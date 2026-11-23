import Combine
import Foundation
import Libbox
import Security
import UIKit
// NetworkExtension 的类型(NETunnelProviderManager 等)还没有做 Sendable 审计,
// 完成 handler 里从系统线程切回 @MainActor 时 Swift 6 严格并发检查会报「sending
// 风险」。@preconcurrency 是 Apple 系统框架里这类未审计类型的标准处理方式。
@preconcurrency import NetworkExtension

// MARK: - TunnelManager (iOS / tvOS)
//
// macOS 版靠 PrivilegeManager 安装 LaunchDaemon + helper,再用 AF_UNIX socket 拨号。
// iOS / tvOS 上没有特权进程:VPN 逻辑跑在 NEPacketTunnelProvider 扩展进程里,由系统托管。
// App 侧只做两件事:
//   1. 通过 NETunnelProviderManager 把一份 VPN 配置写进系统偏好(saveToPreferences),
//      让系统「知道有这么一个隧道可以拨」——这一步会触发用户授权弹窗。
//   2. 通过一次性 start options 交付启动配置，再用 manager.connection
//      (即 NETunnelProviderSession)与扩展进程收发状态/控制消息。
//
// 本文件是「薄骨架」:核心运行时行为(授权弹窗 / on-demand 状态机 / 真实拨号)
// 在没有 NetworkExtension entitlement 和真机之前完全无法离线验证,凡是受签名/
// entitlement 影响的地方都标了 TODO,真机联调时按标记逐一核对。
//
// 依赖对接:
//   - conform `TunnelManaging`(AppState.swift):暴露并刷新 isProfileInstalled。
//   - 持有一个 `TunnelProviderSession`(conform GoEngine.swift 的 `TunnelSession`),
//     注入给 GoEngine.session,让 GoEngine 的 sendProviderMessage 落到真实扩展会话上。

/// bundle id 与 App Group 必须和各平台 target 的 entitlement 一致。
private let kTunnelBundleIdentifier = XDialIdentity.tunnelBundleIdentifier
private let kAppGroupIdentifier = XDialIdentity.appGroup

/// providerConfiguration 里携带的隧道自身描述名。
private let kTunnelLocalizedDescription = "XDial"
private let kTunnelDiagnosticKey = "xdial.tunnel.diagnostic"
private let kMaxNEConfigBytes = 2 * 1024 * 1024

/// NetworkExtension 的类型(NETunnelProviderManager 等)没有 Sendable 审计。这里的用法
/// 是"系统完成回调只调一次、拿到就立刻在同一步用掉",不存在真实并发访问——
/// 用这个 box 显式告诉 Swift 6 的 sending 检查器信任这一点,而不是引入 Task 或改变
/// 回调时序。仅用于跨越 assumeIsolated 边界搬运这几个已知安全的值。
private struct UncheckedBox<T>: @unchecked Sendable {
    let value: T
}

final class OneShotContinuation<Value: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<Value, Never>?
    private var resolved = false
    private var resolvedValue: Value?

    init(_ continuation: CheckedContinuation<Value, Never>? = nil) {
        self.continuation = continuation
    }

    func install(_ continuation: CheckedContinuation<Value, Never>) {
        lock.lock()
        if resolved {
            let value = resolvedValue
            lock.unlock()
            if let value {
                continuation.resume(returning: value)
            }
            return
        }
        self.continuation = continuation
        lock.unlock()
    }

    func finish(_ value: Value) {
        lock.lock()
        guard !resolved else {
            lock.unlock()
            return
        }
        resolved = true
        resolvedValue = value
        let pending = continuation
        continuation = nil
        lock.unlock()
        pending?.resume(returning: value)
    }
}

/// 一次启动只允许一个活跃 token。状态组件只负责原子地登记/领取 completion，
/// completion 在状态修改完成后由调用方执行，避免回调重入时触发独占访问冲突。
struct TunnelStartOperationState {
    typealias Completion = @Sendable (Result<Void, Error>) -> Void

    private var activeID: UUID?
    private var activeCompletion: Completion?

    mutating func begin(completion: @escaping Completion) -> (id: UUID, cancelled: Completion?) {
        let cancelled = activeCompletion
        let id = UUID()
        activeID = id
        activeCompletion = completion
        return (id, cancelled)
    }

    var hasPendingOperation: Bool { activeID != nil }

    func isCurrent(_ id: UUID) -> Bool {
        activeID == id
    }

    mutating func finish(_ id: UUID) -> Completion? {
        guard activeID == id else { return nil }
        let completion = activeCompletion
        activeID = nil
        activeCompletion = nil
        return completion
    }

    mutating func cancel() -> Completion? {
        let completion = activeCompletion
        activeID = nil
        activeCompletion = nil
        return completion
    }
}

/// LibboxGenerateNEConfig 会重建 App Group 内的规则目录，所有启动代际必须在同一条
/// FIFO 队列执行。取消只废弃结果，不能中断正在执行的 Go 调用。
final class TunnelConfigGenerationQueue: @unchecked Sendable {
    private let queue: DispatchQueue

    init(label: String = "com.kafeifei.xdial.config-generation") {
        queue = DispatchQueue(label: label, qos: .userInitiated)
    }

    func submit(_ work: @escaping @Sendable () -> Void) {
        queue.async(execute: work)
    }
}

struct TunnelAcceptanceTarget: Codable, Sendable, Equatable {
    let tag: String
    let label: String
}

struct LineProbeReading: Equatable {
    let label: String
    let result: ConnectionTimeline.Result

    static func warning(_ readings: [Self], isChinese: Bool) -> String? {
        guard readings.contains(where: { $0.result != .ok }) else { return nil }
        return readings.map { reading in
            switch reading.result {
            case .ok: return reading.label + (isChinese ? "探测通过" : " probe passed")
            case .timeout: return reading.label + (isChinese ? "探测超时" : " probe timed out")
            default: return reading.label + (isChinese ? "探测失败" : " probe failed")
            }
        }.joined(separator: isChinese ? "，" : ", ")
    }
}

struct TunnelAcceptancePlan: Codable, Sendable, Equatable {
    let requiresAnyConnect: Bool
    /// nil 只允许用于真正的单出口 Direct 场景。非 nil 时，它是配置页可见的
    /// 1.1.1.1 锁定规则所绑定的确定性非 Direct outbound。
    let currentRouteTag: String?
    let targets: [TunnelAcceptanceTarget]
    /// Go generator emits every enabled Tailscale line as a global endpoint
    /// because preferred_by/accept_routes may affect routing without a scenario
    /// binding. Readiness must cover that exact generated endpoint set.
    let generatedTailscaleTargets: [TunnelAcceptanceTarget]

    static func make(
        profile: Profile,
        profileJSON: String,
        anyConnect: AnyConnectCredentials?
    ) throws -> TunnelAcceptancePlan {
        guard let scenario = profile.scenarios.first(where: { $0.id == profile.activeScenarioID }) else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "当前场景不可用"])
        }

        struct Catalog: Decodable {
            struct Entry: Decodable {
                struct Group: Decodable {
                    let tag: String
                }
                let id: String
                let groups: [Group]
            }
            let subscriptions: [Entry]
        }

        let usesSubscriptions = !scenario.defaultSubscriptionID.isEmpty
            || scenario.bindings.contains { !$0.subscriptionID.isEmpty }
        var subscriptionTags: [String: String] = [:]
        if usesSubscriptions {
            var catalogError: NSError?
            let rawCatalog = LibboxSubscriptionRuntimeCatalog(profileJSON, &catalogError)
            if let catalogError { throw catalogError }
            guard let data = rawCatalog.data(using: .utf8),
                  let catalog = try? JSONDecoder().decode(Catalog.self, from: data) else {
                throw TunnelRuntimeError.invalidCatalog
            }
            for subscription in catalog.subscriptions {
                if let tag = subscription.groups.first?.tag, !tag.isEmpty {
                    subscriptionTags[subscription.id] = tag
                }
            }
        }

        func lineTag(_ lineID: String) -> String? {
            guard let line = profile.lines.first(where: { $0.id == lineID && $0.enabled }) else {
                return nil
            }
            switch line.type {
            case "direct": return "direct"
            case "vpn": return "vpn"
            case "tailscale": return "tailscale-" + line.id
            case "trojan", "shadowsocks", "ss", "vmess", "vless", "anytls", "selector", "urltest": return "proxy-" + line.id
            default: return nil
            }
        }

        func targetTag(lineID: String, subscriptionID: String) -> String? {
            if !subscriptionID.isEmpty {
                return subscriptionTags[subscriptionID]
            }
            return lineTag(lineID)
        }

        func publicTargetTag(lineID: String, subscriptionID: String) -> String? {
            if !subscriptionID.isEmpty {
                return subscriptionTags[subscriptionID]
            }
            guard let line = profile.lines.first(where: {
                $0.id == lineID && $0.enabled
            }) else { return nil }
            if line.type == "tailscale", line.tailscaleExitNode.isEmpty {
                return nil
            }
            return lineTag(lineID)
        }

        let requiresAnyConnect = anyConnect != nil
        let generatedTailscaleTargets: [TunnelAcceptanceTarget] = profile.lines.compactMap {
            line -> TunnelAcceptanceTarget? in
            guard line.enabled, line.type == "tailscale" else { return nil }
            return TunnelAcceptanceTarget(
                tag: "tailscale-" + line.id,
                label: line.name.isEmpty ? "Tailscale" : line.name
            )
        }
        guard targetTag(
            lineID: scenario.defaultLineID,
            subscriptionID: scenario.defaultSubscriptionID
        ) != nil else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "当前场景的默认出口不可用"])
        }

        let expectedOutboundBinding = profile.connectivityOutboundBinding(for: scenario)
        let configuredOutboundBinding = scenario.bindings.first {
            $0.ruleSetID == RuleSet.connectivityOutboundID
        }
        guard expectedOutboundBinding?.lineID == configuredOutboundBinding?.lineID,
              expectedOutboundBinding?.subscriptionID == configuredOutboundBinding?.subscriptionID else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "非 Direct 连接验收规则绑定不正确"])
        }
        let currentRouteTag = configuredOutboundBinding.flatMap {
            targetTag(lineID: $0.lineID, subscriptionID: $0.subscriptionID)
        }
        if currentRouteTag == "direct" {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "非 Direct 连接验收规则不能绑定 Direct"])
        }

        var targets: [TunnelAcceptanceTarget] = []
        var seen = Set<String>()
        func addTarget(lineID: String, subscriptionID: String) {
            guard let tag = publicTargetTag(
                lineID: lineID,
                subscriptionID: subscriptionID
            ),
                  seen.insert(tag).inserted else { return }
            let label: String
            if !subscriptionID.isEmpty {
                label = profile.subscriptions.first(where: { $0.id == subscriptionID })?.name
                    ?? subscriptionID
            } else {
                let line = profile.lines.first(where: { $0.id == lineID })
                label = line?.type == "vpn" ? "AnyConnect" : (line?.name ?? lineID)
            }
            targets.append(TunnelAcceptanceTarget(tag: tag, label: label))
        }
        addTarget(lineID: scenario.defaultLineID, subscriptionID: scenario.defaultSubscriptionID)
        for binding in scenario.bindings {
            guard profile.ruleSets.first(where: { $0.id == binding.ruleSetID })?.enabled == true else {
                continue
            }
            addTarget(lineID: binding.lineID, subscriptionID: binding.subscriptionID)
        }
        guard !targets.isEmpty else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "当前场景没有可验收的出口"])
        }
        if targets.count > 1 && currentRouteTag == nil {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "多出口场景缺少非 Direct 路由验收目标"])
        }
        if targets.count == 1 && targets[0].tag != "direct" {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "单出口验收只允许 Direct 场景"])
        }
        return TunnelAcceptancePlan(
            requiresAnyConnect: requiresAnyConnect,
            currentRouteTag: currentRouteTag,
            targets: targets,
            generatedTailscaleTargets: generatedTailscaleTargets
        )
    }
}

/// 从主 App 发起探测，流量会像 Safari/其他 App 一样完整经过系统路由 → packetFlow →
/// sing-box → 实际出口。扩展进程内部自拨成功不能替代这条端到端断言。
enum DataPathProbe {
    private static let directURL = URL(string: "https://1.0.0.1/cdn-cgi/trace")!
    private static let hostnameURL = URL(string: "https://www.apple.com/library/test/success.html")!
    private static let currentURL = URL(string: "https://1.1.1.1/cdn-cgi/trace")!

    /// Outcome and duration of one probe request, for the connection timeline.
    struct CheckRecord: Sendable, Equatable {
        let result: ConnectionTimeline.Result
        let ms: Int
        var completedAt: Int64 = ConnectionTimeline.nowMilliseconds()
        var urlErrorCode: Int?
        var errorDetail: String { urlErrorCode.map { " NSURLError=\($0)" } ?? "" }

        static func failed(_ error: Error, since start: TimeInterval) -> Self {
            let nsError = error as NSError
            return Self(result: ConnectionTimeline.classify(error), ms: ConnectionTimeline.elapsedMilliseconds(since: start),
                        urlErrorCode: nsError.domain == NSURLErrorDomain ? nsError.code : nil)
        }

        static let notRun = CheckRecord(result: .notRun, ms: 0)
    }

    struct Result: Sendable {
        let directLineEgressIP: String?
        let anyConnectLineEgressIP: String?
        let routedDirectEgressIP: String?
        let routedAnyConnectEgressIP: String?
        let hostnameOK: Bool
        let routingEvidenceOK: Bool
        var directCheck: CheckRecord = .notRun
        var currentCheck: CheckRecord = .notRun
        var hostnameCheck: CheckRecord = .notRun

        var linesReachable: Bool {
            directLineEgressIP != nil && anyConnectLineEgressIP != nil
        }

        /// 公网出口地址只做展示，不做路由判定：同一线路可能使用多个 NAT 出口，
        /// 两条线路也可能恰好共用一个公网出口。分流成功以 libbox Router 对两个
        /// 固定探针目标记录的实际 outbound 命中计数为准。
        var splitRoutingOK: Bool { routingEvidenceOK }

        var isUsable: Bool { linesReachable && splitRoutingOK && hostnameOK }

        var diagnosticSummary: String {
            let directLine = directLineEgressIP ?? "不可用"
            let anyConnectLine = anyConnectLineEgressIP ?? "不可用"
            let routedDirect = routedDirectEgressIP ?? "不可用"
            let routedAnyConnect = routedAnyConnectEgressIP ?? "不可用"
            return "线路探测：Direct \(directLine) · AnyConnect \(anyConnectLine)；"
                + "分流探测：1.0.0.1→\(routedDirect) · 1.1.1.1→\(routedAnyConnect)；"
                + "路由命中：\(splitRoutingOK ? "通过" : "未通过")；域名 HTTPS：\(hostnameOK ? "通过" : "未通过")"
        }

        var failureMessage: String {
            if anyConnectLineEgressIP == nil {
                return "AnyConnect 线路不通，已自动断开"
            }
            if directLineEgressIP == nil {
                return "Direct 线路不通，已自动断开"
            }
            if routedDirectEgressIP == nil || routedAnyConnectEgressIP == nil {
                return "分流测试地址不可达，已自动断开"
            }
            if !splitRoutingOK {
                return "分流器未按当前配置选择线路，已自动断开"
            }
            if !hostnameOK {
                return "系统隧道已启动，但域名 HTTPS 请求失败，已自动断开"
            }
            return "系统隧道已启动，但出口流量不通，已自动断开"
        }

        func withRoutingEvidence(_ accepted: Bool) -> Result {
            Result(
                directLineEgressIP: directLineEgressIP,
                anyConnectLineEgressIP: anyConnectLineEgressIP,
                routedDirectEgressIP: routedDirectEgressIP,
                routedAnyConnectEgressIP: routedAnyConnectEgressIP,
                hostnameOK: hostnameOK,
                routingEvidenceOK: accepted,
                directCheck: directCheck,
                currentCheck: currentCheck,
                hostnameCheck: hostnameCheck
            )
        }
    }

    static func run(
        directLineEgressIP: String?,
        anyConnectLineEgressIP: String?,
        includeSplitTarget: Bool = true
    ) async -> Result {
        async let directTrace = trace(directURL)
        async let hostnameOK = httpsCheck(probeRequest(hostnameURL))
        let anyConnect = includeSplitTarget
            ? await trace(currentURL)
            : TraceResult(reachable: false, address: nil, check: .notRun)
        let (direct, hostname) = await (directTrace, hostnameOK)
        return Result(
            directLineEgressIP: directLineEgressIP,
            anyConnectLineEgressIP: anyConnectLineEgressIP,
            routedDirectEgressIP: direct.address,
            routedAnyConnectEgressIP: anyConnect.address,
            hostnameOK: hostname.result == .ok,
            routingEvidenceOK: false,
            directCheck: direct.check,
            currentCheck: anyConnect.check,
            hostnameCheck: hostname
        )
    }

    struct TraceResult: Sendable {
        let reachable: Bool
        let address: String?
        var check: CheckRecord = .notRun
    }

    private static func trace(_ url: URL) async -> TraceResult {
        let session = URLSession(configuration: probeConfiguration())
        defer { session.invalidateAndCancel() }
        let startedAt = ProcessInfo.processInfo.systemUptime
        do {
            let (data, response) = try await session.data(for: probeRequest(url))
            let http = response as? HTTPURLResponse
            let address = http.flatMap { (200..<300).contains($0.statusCode) ? parseTraceIPAddress(data) : nil }
            let check = CheckRecord(result: address == nil ? .http : .ok,
                                    ms: ConnectionTimeline.elapsedMilliseconds(since: startedAt))
            return TraceResult(reachable: address != nil, address: address, check: check)
        } catch {
            return TraceResult(reachable: false, address: nil, check: .failed(error, since: startedAt))
        }
    }

    static func parseTraceIPAddress(_ data: Data) -> String? {
        guard let text = String(data: data, encoding: .utf8) else { return nil }
        let allowed = CharacterSet(charactersIn: "0123456789abcdefABCDEF:.")
        for line in text.split(whereSeparator: \.isNewline) {
            guard line.hasPrefix("ip=") else { continue }
            let address = String(line.dropFirst(3)).trimmingCharacters(in: .whitespacesAndNewlines)
            guard !address.isEmpty,
                  address.utf8.count <= 64,
                  address.rangeOfCharacter(from: allowed.inverted) == nil,
                  IPv4Address(address) != nil || IPv6Address(address) != nil else { return nil }
            return address
        }
        return nil
    }

    // A HEAD response (including 404) proves TLS reached this independent IP.
    // Only the router counters can say which Line carried the request.
    static func alternateHTTPSCheck() async -> CheckRecord {
        var request = probeRequest(URL(string: "https://223.5.5.5/")!)
        request.httpMethod = "HEAD"
        request.timeoutInterval = 3
        return await httpsCheck(request, requireOriginalTarget: true)
    }

    private static func httpsCheck(_ request: URLRequest, requireOriginalTarget: Bool = false) async -> CheckRecord {
        let session = URLSession(configuration: probeConfiguration())
        defer { session.invalidateAndCancel() }
        let startedAt = ProcessInfo.processInfo.systemUptime
        do {
            let (_, response) = try await session.data(for: request)
            let http = response as? HTTPURLResponse
            let accepted = http.map { (200..<500).contains($0.statusCode) } == true
                && (!requireOriginalTarget || response.url == request.url)
            return CheckRecord(result: accepted ? .ok : .http,
                               ms: ConnectionTimeline.elapsedMilliseconds(since: startedAt))
        } catch {
            return .failed(error, since: startedAt)
        }
    }

    private static func probeConfiguration() -> URLSessionConfiguration {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.waitsForConnectivity = false
        configuration.timeoutIntervalForRequest = 12
        configuration.timeoutIntervalForResource = 15
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        return configuration
    }

    private static func probeRequest(_ url: URL) -> URLRequest {
        var request = URLRequest(url: url)
        request.timeoutInterval = 12
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        return request
    }
}

/// 路由命中自检没过时的明细，例如
/// 「；命中明细：1.0.0.1 直连×1 · 1.1.1.1 没有记到；测试请求：1.0.0.1 无回应 · 1.1.1.1 回应 203.0.113.9」。
func routingHitDetail(
    before: RoutingProbeSnapshot?,
    after: RoutingProbeSnapshot?,
    labels: [String: String],
    directReply: String?,
    currentReply: String??
) -> String {
    func reply(_ address: String?) -> String {
        address.map { "回应 \($0)" } ?? "无回应"
    }
    var replies = "1.0.0.1 \(reply(directReply))"
    if let currentReply { replies += " · 1.1.1.1 \(reply(currentReply))" }

    guard let before, let after else {
        return "；命中明细：VPN 扩展没有返回计数；测试请求：\(replies)"
    }
    func hits(_ old: [String: Int64], _ new: [String: Int64]) -> String {
        let parts = Set(old.keys).union(new.keys).sorted().compactMap { tag -> String? in
            let delta = (new[tag] ?? 0) - (old[tag] ?? 0)
            guard delta != 0 else { return nil }
            let name = tag.isEmpty ? "未匹配到线路" : (labels[tag] ?? tag)
            return "\(name)×\(delta)"
        }
        return parts.isEmpty ? "没有记到" : parts.joined(separator: "、")
    }
    return "；命中明细：1.0.0.1 \(hits(before.directTargetTags, after.directTargetTags))"
        + " · 1.1.1.1 \(hits(before.anyConnectTargetTags, after.anyConnectTargetTags))"
        + "；测试请求：\(replies)"
}

enum SplitRoutingAcceptanceState: String, Sendable, Equatable {
    case passed
    case failed
    case notApplicable
}

struct ConfiguredDataPathResult: Sendable {
    let targetAddresses: [String: String]
    let targets: [TunnelAcceptanceTarget]
    let unavailableTargets: [TunnelAcceptanceTarget]
    let routedDirectEgressIP: String?
    let routedCurrentEgressIP: String?
    let hostnameOK: Bool
    let routingEvidenceOK: Bool
    let currentRouteTag: String?

    init(
        targetAddresses: [String: String],
        targets: [TunnelAcceptanceTarget] = [],
        unavailableTargets: [TunnelAcceptanceTarget],
        routedDirectEgressIP: String?,
        routedCurrentEgressIP: String?,
        hostnameOK: Bool,
        routingEvidenceOK: Bool,
        currentRouteTag: String?
    ) {
        self.targetAddresses = targetAddresses
        self.targets = targets
        self.unavailableTargets = unavailableTargets
        self.routedDirectEgressIP = routedDirectEgressIP
        self.routedCurrentEgressIP = routedCurrentEgressIP
        self.hostnameOK = hostnameOK
        self.routingEvidenceOK = routingEvidenceOK
        self.currentRouteTag = currentRouteTag
    }

    var splitRoutingState: SplitRoutingAcceptanceState {
        if currentRouteTag == nil {
            return .notApplicable
        }
        return routingEvidenceOK ? .passed : .failed
    }

    var isUsable: Bool {
        unavailableTargets.isEmpty
            && hostnameOK
            && routingEvidenceOK
    }

    var diagnosticSummary: String {
        let targetLabels = Dictionary(
            uniqueKeysWithValues: targets.map { ($0.tag, $0.label) }
        )
        let checkedTargets = targetAddresses.keys.sorted().map { tag in
            let label = targetLabels[tag] ?? Self.safeTargetLabel(for: tag)
            return "\(label) \(targetAddresses[tag] ?? "不可用")"
        }.joined(separator: " · ")
        let unavailable = unavailableTargets.map(\.label).joined(separator: "、")
        let lineSummary = unavailable.isEmpty
            ? checkedTargets
            : "\(checkedTargets)；不可用：\(unavailable)"
        if currentRouteTag == nil, isUsable {
            return "线路探测：\(lineSummary)；公网双出口不适用；线路与域名 HTTPS 通过，未执行双出口分流验收"
        }
        let currentRouteLabel = currentRouteTag.map {
            targetLabels[$0] ?? Self.safeTargetLabel(for: $0)
        } ?? "不适用"
        return "线路探测：\(lineSummary)；分流规则：1.0.0.1→Direct · 1.1.1.1→"
            + "\(currentRouteLabel)；"
            + "路由命中：\(routingEvidenceOK ? "通过" : "未通过")；"
            + "域名 HTTPS：\(hostnameOK ? "通过" : "未通过")"
    }

    private static func safeTargetLabel(for tag: String) -> String {
        if tag == "direct" { return "Direct" }
        if tag == "vpn" { return "AnyConnect" }
        if tag.hasPrefix("tailscale-") { return "Tailscale" }
        if tag.hasPrefix("proxy-") { return "Proxy" }
        return "Subscription"
    }

    var failureMessage: String {
        if !unavailableTargets.isEmpty {
            return "线路“\(unavailableTargets.map(\.label).joined(separator: "、"))”不通，已自动断开"
        }
        if !routingEvidenceOK {
            return currentRouteTag == nil
                ? "Direct 路由验收失败，已自动断开"
                : "分流器未按当前配置选择线路，已自动断开"
        }
        if !hostnameOK {
            return "系统隧道已启动，但域名 HTTPS 请求失败，已自动断开"
        }
        return "系统隧道已启动，但出口流量不通，已自动断开"
    }
}

/// 扩展 `traffic-snapshot` 的应答，对应 Go `Libbox.TrafficTotals`。
/// 只单调递增；新一次隧道启动会从 0 重新计数。
struct TrafficTotals: Decodable, Sendable, Equatable {
    let uploadBytes: Int64
    let downloadBytes: Int64

    enum CodingKeys: String, CodingKey {
        case uploadBytes = "upload_bytes"
        case downloadBytes = "download_bytes"
    }
}

struct RoutingProbeSnapshot: Decodable, Sendable, Equatable {
    let alternateHTTPSDirect: Int64
    let alternateHTTPSOther: Int64
    let directTargetDirect: Int64
    let directTargetVPN: Int64
    let directTargetOther: Int64
    let anyConnectTargetDirect: Int64
    let anyConnectTargetVPN: Int64
    let anyConnectTargetOther: Int64
    let directTargetTags: [String: Int64]
    let anyConnectTargetTags: [String: Int64]

    enum CodingKeys: String, CodingKey {
        case alternateHTTPSDirect = "alternate_https_direct"
        case alternateHTTPSOther = "alternate_https_other"
        case directTargetDirect = "direct_target_direct"
        case directTargetVPN = "direct_target_vpn"
        case directTargetOther = "direct_target_other"
        case anyConnectTargetDirect = "anyconnect_target_direct"
        case anyConnectTargetVPN = "anyconnect_target_vpn"
        case anyConnectTargetOther = "anyconnect_target_other"
        case directTargetTags = "direct_target_tags"
        case anyConnectTargetTags = "anyconnect_target_tags"
    }

    init(
        directTargetDirect: Int64,
        directTargetVPN: Int64,
        directTargetOther: Int64,
        anyConnectTargetDirect: Int64,
        anyConnectTargetVPN: Int64,
        anyConnectTargetOther: Int64,
        alternateHTTPSDirect: Int64 = 0,
        alternateHTTPSOther: Int64 = 0,
        directTargetTags: [String: Int64] = [:],
        anyConnectTargetTags: [String: Int64] = [:]
    ) {
        self.alternateHTTPSDirect = alternateHTTPSDirect
        self.alternateHTTPSOther = alternateHTTPSOther
        self.directTargetDirect = directTargetDirect
        self.directTargetVPN = directTargetVPN
        self.directTargetOther = directTargetOther
        self.anyConnectTargetDirect = anyConnectTargetDirect
        self.anyConnectTargetVPN = anyConnectTargetVPN
        self.anyConnectTargetOther = anyConnectTargetOther
        self.directTargetTags = directTargetTags
        self.anyConnectTargetTags = anyConnectTargetTags
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        alternateHTTPSDirect = try container.decodeIfPresent(Int64.self, forKey: .alternateHTTPSDirect) ?? 0
        alternateHTTPSOther = try container.decodeIfPresent(Int64.self, forKey: .alternateHTTPSOther) ?? 0
        directTargetDirect = try container.decode(Int64.self, forKey: .directTargetDirect)
        directTargetVPN = try container.decode(Int64.self, forKey: .directTargetVPN)
        directTargetOther = try container.decode(Int64.self, forKey: .directTargetOther)
        anyConnectTargetDirect = try container.decode(Int64.self, forKey: .anyConnectTargetDirect)
        anyConnectTargetVPN = try container.decode(Int64.self, forKey: .anyConnectTargetVPN)
        anyConnectTargetOther = try container.decode(Int64.self, forKey: .anyConnectTargetOther)
        directTargetTags = try container.decodeIfPresent(
            [String: Int64].self,
            forKey: .directTargetTags
        ) ?? [:]
        anyConnectTargetTags = try container.decodeIfPresent(
            [String: Int64].self,
            forKey: .anyConnectTargetTags
        ) ?? [:]
    }

    func alternateHTTPSRoute(after next: Self) -> String {
        if next.alternateHTTPSDirect > alternateHTTPSDirect, next.alternateHTTPSOther == alternateHTTPSOther {
            return "direct"
        }
        if next.alternateHTTPSOther > alternateHTTPSOther, next.alternateHTTPSDirect == alternateHTTPSDirect {
            return "other"
        }
        return "unknown"
    }

    /// 兼容旧的 Direct + AnyConnect 计数断言；新路径使用下面按 exact tag 的证据。
    func provesConfiguredRouting(after current: RoutingProbeSnapshot) -> Bool {
        func delta(_ newer: Int64, _ older: Int64) -> Int64? {
            guard newer >= older else { return nil }
            return newer - older
        }
        guard let directExpected = delta(current.directTargetDirect, directTargetDirect),
              let directWrongVPN = delta(current.directTargetVPN, directTargetVPN),
              let directWrongOther = delta(current.directTargetOther, directTargetOther),
              let anyWrongDirect = delta(current.anyConnectTargetDirect, anyConnectTargetDirect),
              let anyExpected = delta(current.anyConnectTargetVPN, anyConnectTargetVPN),
              let anyWrongOther = delta(current.anyConnectTargetOther, anyConnectTargetOther) else {
            return false
        }
        return directExpected > 0 && anyExpected > 0
            && directWrongVPN == 0 && directWrongOther == 0
            && anyWrongDirect == 0 && anyWrongOther == 0
    }

    func provesConfiguredRouting(
        after current: RoutingProbeSnapshot,
        directTag: String,
        currentRouteTag: String
    ) -> Bool {
        func provesOnlyExpectedTag(
            before: [String: Int64],
            after: [String: Int64],
            expectedTag: String
        ) -> Bool {
            let tags = Set(before.keys).union(after.keys)
            var expectedDelta: Int64 = 0
            for tag in tags {
                let oldValue = before[tag] ?? 0
                let newValue = after[tag] ?? 0
                guard newValue >= oldValue else { return false }
                let delta = newValue - oldValue
                if tag == expectedTag {
                    expectedDelta = delta
                } else if delta != 0 {
                    return false
                }
            }
            return expectedDelta > 0
        }
        return provesOnlyExpectedTag(
            before: directTargetTags,
            after: current.directTargetTags,
            expectedTag: directTag
        ) && provesOnlyExpectedTag(
            before: anyConnectTargetTags,
            after: current.anyConnectTargetTags,
            expectedTag: currentRouteTag
        )
    }

    /// 单出口 Direct 场景仍证明系统 URLSession 确实经过配置中的 Direct 规则，
    /// 但不把这条单路由证据包装成“分流通过”。
    func provesConfiguredDirectRouting(
        after current: RoutingProbeSnapshot,
        directTag: String
    ) -> Bool {
        let tags = Set(directTargetTags.keys).union(current.directTargetTags.keys)
        var expectedDelta: Int64 = 0
        for tag in tags {
            let oldValue = directTargetTags[tag] ?? 0
            let newValue = current.directTargetTags[tag] ?? 0
            guard newValue >= oldValue else { return false }
            let delta = newValue - oldValue
            if tag == directTag {
                expectedDelta = delta
            } else if delta != 0 {
                return false
            }
        }
        return expectedDelta > 0
    }
}

enum ProbeFailureReconnectPolicy {
    /// A diagnostics-only failure pass must never affect a later connection.
    /// Suppression belongs only to the branch that is about to stop an active
    /// tunnel because this acceptance attempt failed.
    static func shouldSuppressAutomaticReconnect(shouldStop: Bool, tunnelIsActive: Bool) -> Bool {
        shouldStop && tunnelIsActive
    }
}

enum SystemOnDemandReconnectPolicy {
    static func shouldArm(
        userEnabled: Bool,
        tunnelReady: Bool,
        hasStartEnvelope: Bool
    ) -> Bool {
        userEnabled && tunnelReady && hasStartEnvelope
    }

    static func shouldSuspendActiveProfile(
        systemOnDemandActive: Bool,
        userEnabled: Bool,
        hasStartEnvelope: Bool
    ) -> Bool {
        systemOnDemandActive && (!userEnabled || !hasStartEnvelope)
    }
}

enum TunnelDiagnosticFormatter {
    static func recordLaunchRequested(attemptID: String) {
        let snapshot: [String: Any] = [
            "stage": "launch_requested",
            "attempt_id": attemptID,
            "timestamp": Date().timeIntervalSince1970,
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: snapshot),
              let text = String(data: data, encoding: .utf8),
              let defaults = UserDefaults(suiteName: kAppGroupIdentifier) else { return }
        defaults.set(text, forKey: kTunnelDiagnosticKey)
        defaults.synchronize()
    }

    static func persistedSnapshot(matchingAttemptID expected: String?) -> [String: Any]? {
        guard let text = UserDefaults(suiteName: kAppGroupIdentifier)?.string(forKey: kTunnelDiagnosticKey),
              let data = text.data(using: .utf8),
              let snapshot = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              expected == nil || snapshot["attempt_id"] as? String == expected else { return nil }
        return snapshot
    }

    static func persistedSummary(matchingAttemptID expected: String? = nil) -> String? {
        persistedSnapshot(matchingAttemptID: expected).flatMap { summary($0) }
    }

    static func providerSummary(
        from responseData: Data?,
        matchingAttemptID expectedAttemptID: String? = nil
    ) -> String? {
        guard let responseData,
              let outer = try? JSONSerialization.jsonObject(with: responseData) as? [String: Any],
              let dataString = outer["data"] as? String,
              let data = dataString.data(using: .utf8),
              let status = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let diagnostics = status["diagnostics"] as? [String: Any] else {
            return nil
        }
        return summary(diagnostics, matchingAttemptID: expectedAttemptID)
    }

    static func summary(
        _ snapshot: [String: Any],
        matchingAttemptID expectedAttemptID: String? = nil
    ) -> String? {
        if let expectedAttemptID,
           snapshot["attempt_id"] as? String != expectedAttemptID {
            return nil
        }
        var parts: [String] = []
        let topLevelError = nonEmptyString(snapshot["last_error"])
        if let topLevelError {
            parts.append("error=\(topLevelError)")
        }
        if let stage = snapshot["stage"] as? String, !stage.isEmpty {
            parts.append("stage=\(stage)")
        }
        let defaultInterfaceName = nonEmptyString(snapshot["default_interface_name"])
            ?? nonEmptyString(snapshot["physical_interface_name"])
        if let name = defaultInterfaceName {
            parts.append("if=\(name)")
        }
        if let engine = snapshot["engine"] as? [String: Any] {
            if let engineError = nonEmptyString(engine["last_error"]),
               engineError != topLevelError {
                parts.append("engine_error=\(engineError)")
            }
            if engine["gvisor_compiled"] as? Bool == false {
                parts.append("gvisor=missing")
            } else if let stack = engine["selected_stack"] as? String {
                parts.append("stack=\(stack)")
            }
            if let tunName = engine["tun_name"] as? String, !tunName.isEmpty {
                parts.append("tun=\(tunName)")
            } else if engine["tun_fd_ready"] as? Bool == true {
                parts.append("tun=fd-ready")
            }
            let tunIn = number(engine["tun_in_packets"])
            let tunOut = number(engine["tun_out_packets"])
            parts.append("flow=in\(tunIn)/out\(tunOut)")
            if let bridge = engine["bridge"] as? [String: Any] {
                let up = number(bridge["up_packets"])
                let down = number(bridge["down_packets"])
                parts.append("bridge=↑\(up)/↓\(down)")
            }
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private static func number(_ value: Any?) -> Int {
        (value as? NSNumber)?.intValue ?? 0
    }

    private static func nonEmptyString(_ value: Any?) -> String? {
        guard let value = value as? String, !value.isEmpty else { return nil }
        return value
    }
}

// MARK: - TunnelProviderSession
//
// GoEngine 的 `session` 是 `weak var session: TunnelSession?`,它只认识
// `sendProviderMessage(_:responseHandler:)` 这一个方法。这里用一个独立的小类包住
// 真实的 `NETunnelProviderSession`,而不是让 TunnelManager 直接 conform ——
// 好处是 GoEngine 弱引用的目标(session)与 AppState 弱引用的目标(tunnelManager)
// 是两个不同对象,由 TunnelManager 分别强持有,生命周期清晰、互不牵连。

/// 把 GoEngine 需要的 `TunnelSession` 转调到真实的 `NETunnelProviderSession`。
@MainActor
final class TunnelProviderSession: TunnelSession {
    /// 真实会话。来自 `NETunnelProviderManager.connection as? NETunnelProviderSession`。
    /// 隧道尚未 save / 未拨起时可能为 nil,此时发送直接抛错(语义等价 macOS 版 socket 不可用)。
    fileprivate weak var session: NETunnelProviderSession?

    init(session: NETunnelProviderSession?) {
        self.session = session
    }

    // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
    // sendProviderMessage 只有在扩展进程真正在运行(connection 状态 connecting/connected)
    // 时才会把消息投递到 handleAppMessage;若隧道未拨起,系统可能直接抛错或静默丢弃。
    // GoEngine 里 start/stop/status 全靠这条通道,真机上要确认:未连接时能不能发 status、
    // 发送时机与 connection.status 的关系、responseHandler 回调所在线程。
    func sendProviderMessage(_ messageData: Data, responseHandler: ((Data?) -> Void)?) throws {
        guard let session else {
            throw NSError(domain: "XDial", code: -1,
                          userInfo: [NSLocalizedDescriptionKey: "系统隧道会话不可用(未加载/未拨起)"])
        }
        try session.sendProviderMessage(messageData, responseHandler: responseHandler)
    }
}

// MARK: - TunnelManager

/// NETunnelProviderManager 管理层。负责:加载已有配置、组装 & 保存新配置、把真实
/// 扩展会话喂给 GoEngine。conform `TunnelManaging` 供 AppState 查询「是否已安装」。
/// 连接验收没通过、但隧道保留时，写在 dataPathSummary 开头的标记，首页据此提示。
let dataPathDegradedPrefix = "自检未通过，连接已保留："

/// 前后台与补测预算由同一个值状态管理；计时任务只执行这里允许的下一次补测。
/// 安排计时不消耗次数，真正开始补测才消耗；后台及断开会取消外部计时任务。
struct DataPathRecheckPolicy {
    static let delays: [UInt64] = [30, 120, 300]
    private(set) var isActive: Bool
    private(set) var attempts = 0
    private(set) var isDegraded = false

    init(isActive: Bool) { self.isActive = isActive }

    var shouldDeferProbe: Bool { !isActive }
    var nextDelay: UInt64? {
        guard isActive, isDegraded, attempts < Self.delays.count else { return nil }
        return Self.delays[attempts]
    }

    mutating func enteredBackground() { isActive = false }
    mutating func becameActive() {
        isActive = true
        resetBudget()
    }
    mutating func resetBudget() { attempts = 0 }
    mutating func markDegraded() { isDegraded = true }
    mutating func clearDegraded() {
        isDegraded = false
        resetBudget()
    }
    mutating func beginScheduledRecheck() -> Bool {
        guard nextDelay != nil else { return false }
        attempts += 1
        return true
    }
}

@MainActor
final class TunnelManager: ObservableObject, TunnelManaging {
    /// 手机网络经常高延迟或受限，验收失败时保留隧道而不是主动断开。
    static let keepsTunnelWhenAcceptanceFails = true
    /// 一次验收最多给多久（等用户登录 Tailscale 的时间不算在内）。
    static let dataPathWatchdogSeconds: UInt64 = 90
    /// 自检没通过、连接保留后，App 在前台时按这些间隔（秒）自动再测；用完就停，
    /// 等下次回到前台、手动「重新检测」或系统重连时再从头开始。
    static let degradedRecheckDelays = DataPathRecheckPolicy.delays


    private static let configGenerationQueue = TunnelConfigGenerationQueue()

    /// 当前持有的 manager。loadAllFromPreferences 拿到已有的,或 ensure 时新建。
    @Published private(set) var manager: NETunnelProviderManager?

    /// 已保存并可用的隧道配置是否存在。AppState 用它更新 helperInstalled。
    @Published private(set) var isProfileInstalled: Bool = false
    @Published private(set) var systemOnDemandActive = false
    @Published private(set) var systemOnDemandState: SystemOnDemandState = .disabled
    var systemOnDemandStatePublisher: AnyPublisher<SystemOnDemandState, Never> {
        $systemOnDemandState.eraseToAnyPublisher()
    }

    /// 包给 GoEngine 用的会话封装。每次 manager/connection 变化时重建并重新注入。
    private(set) var providerSession: TunnelProviderSession?

    /// GoEngine 引用,用于注入 session。弱引用避免环(GoEngine 也不强持有本类)。
    private weak var engine: GoEngine?
    private var statusSubscription: AnyCancellable?
    private var observedSystemStatus: NEVPNStatus = .invalid
    private var dataPathProbeTask: Task<Void, Never>?
    private var dataPathProbeID: UUID?
    private var dataPathVerified = false
    /// 正在进行的验收是对已保留连接的补测：不把首页拉回「检查中」，失败也不断开。
    private var dataPathProbeIsRecheck = false
    /// 验收正在跑的步骤，超时时写进诊断，说明停在了哪一步。
    private var dataPathProbeStage: String?
    private var probeStartedAt = ProcessInfo.processInfo.systemUptime
    private var probeCheckpoints: [String] = []

    private func recordProbeCheckpoint(_ detail: String) {
        guard !Task.isCancelled else { return }
        let elapsed = ProcessInfo.processInfo.systemUptime - probeStartedAt
        probeCheckpoints.append(String(format: "+%.1fs %@", elapsed, detail))
    }
    /// Tailscale 等用户登录期间验收会长时间挂着，这时切到后台（去浏览器登录）不能暂停它。
    private var dataPathAwaitingTailscaleLogin = false
    private var dataPathRecheckTask: Task<Void, Never>?
    private var recheckPolicy = DataPathRecheckPolicy(isActive: false)
    private var appLifecycleSubscriptions: Set<AnyCancellable> = []
    private var startOperationState = TunnelStartOperationState()
    private var activeStartAttemptID: String?
    private(set) var activeAcceptancePlan: TunnelAcceptancePlan?
    private var systemOnDemandRequested = false
    private var systemOnDemandSuspendedForSetup = false
    private var onDemandSaveID: UUID?
    private var pendingOnDemandEnvelope: OnDemandStartEnvelope?
    private var activeStartAllowsSystemOnDemand = true
    private var startWatchdogTask: Task<Void, Never>?
    /// Shared app/extension connection timeline (see ConnectionTimeline).
    private let timeline = ConnectionTimelineStore.shared(appGroup: kAppGroupIdentifier, writer: .app)
    /// Session the next timeline events belong to; equals the start attempt ID.
    private var timelineSessionID: String?
    private var checkStartedAt = ProcessInfo.processInfo.systemUptime

    /// Appends an app event to the connection timeline. When the system started
    /// the tunnel itself the app attaches to the extension's active session.
    private func recordTimeline(
        _ stage: String,
        result: ConnectionTimeline.Result? = nil,
        ms: Int? = nil,
        detail: String? = nil,
        at timestamp: Int64? = nil
    ) {
        guard let timeline else { return }
        // Resolve the extension-owned marker at append time. This also adopts
        // system reconnects missed while suspended, without a main-thread read.
        // A manual start keeps its own ID until the system reaches connected.
        let manualStart = startOperationState.hasPendingOperation ||
            (activeStartAttemptID != nil && manager?.connection.status != .connected)
        timeline.append(session: timelineSessionID ?? "", preferActiveSession: !manualStart,
                        source: .app, stage: "app." + stage,
                        result: result, ms: ms, detail: detail, at: timestamp)
    }

    /// The bounded, redacted connection timeline for the diagnostics report.
    func connectionTimelineSnapshot() async -> ConnectionTimeline.File? {
        guard let snapshot = await timeline?.loadSnapshot() else { return nil }
        switch manager?.connection.status {
        case .disconnected?, .invalid?:
            return snapshot.reportingDisconnected()
        default:
            return snapshot
        }
    }

    func recordUserAction(_ action: String) {
        recordTimeline("user." + action)
    }

    init(engine: GoEngine? = nil) {
        self.engine = engine
        // 新版本的启动参数只通过 startTunnel(options:) 一次性交付。应用一启动就
        // 清掉旧版留在 App Group 的明文，不能等到用户下次连接才迁移；否则升级后
        // 长期不连接的设备会一直保留旧账号、密码与完整配置。
        clearLegacyStartOptions()
        statusSubscription = NotificationCenter.default
            .publisher(for: .NEVPNStatusDidChange)
            .receive(on: DispatchQueue.main)
            .sink { [weak self] notification in
                guard let self,
                      let connection = notification.object as? NEVPNConnection,
                      connection === self.manager?.connection else { return }
                self.applyConnectionStatus()
            }
        recheckPolicy = DataPathRecheckPolicy(isActive: UIApplication.shared.applicationState == .active)
        observeAppLifecycle()
    }

    /// 验收探针跑在主 App 里。App 切到后台会被系统挂起，正在跑的请求会超时或失败，
    /// 回来时看门狗多半已经到点，于是把能用的连接误判成「连接验收超时」。
    /// 所以进后台时暂停验收（连接照常保留），回到前台再从头测。
    private func observeAppLifecycle() {
        NotificationCenter.default
            .publisher(for: UIApplication.didEnterBackgroundNotification)
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.handleAppDidEnterBackground() }
            .store(in: &appLifecycleSubscriptions)
        NotificationCenter.default
            .publisher(for: UIApplication.didBecomeActiveNotification)
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.handleAppDidBecomeActive() }
            .store(in: &appLifecycleSubscriptions)
    }

    private func handleAppDidEnterBackground() {
        recheckPolicy.enteredBackground()
        dataPathRecheckTask?.cancel()
        dataPathRecheckTask = nil
        guard dataPathProbeID != nil,
              !dataPathAwaitingTailscaleLogin,
              let connection = manager?.connection,
              connection.status == .connected else { return }
        dataPathProbeTask?.cancel()
        appLog("DataPathProbe: paused, app entered background at \(dataPathProbeStage ?? "start")")
        recordTimeline("check.cancelled", result: .cancelled,
                       ms: ConnectionTimeline.elapsedMilliseconds(since: checkStartedAt),
                       detail: "background at \(dataPathProbeStage ?? "start")")
        // 补测被打断时保留上一次没通过的原因；首次验收被打断则说明是暂停而不是失败。
        settleDegraded(
            summary: dataPathProbeIsRecheck
                ? nil
                : "App 切到了后台，自检暂停；回到前台会自动重新检测",
            connection: connection
        )
    }

    private func handleAppDidBecomeActive() {
        recheckPolicy.becameActive()
        recheckDegradedDataPath(resetBackoff: true)
    }

    func recheckDataPath() {
        recheckDegradedDataPath(resetBackoff: true)
    }

    /// 只补测「已连接、自检没通过、当前没有验收在跑」的连接。resetBackoff 用于用户
    /// 主动触发（回到前台、点重新检测），让自动补测的间隔从头算。
    private func recheckDegradedDataPath(resetBackoff: Bool) {
        guard !recheckPolicy.shouldDeferProbe,
              recheckPolicy.isDegraded, !dataPathVerified, dataPathProbeID == nil,
              let connection = manager?.connection,
              connection.status == .connected else { return }
        if resetBackoff {
            recheckPolicy.resetBudget()
            dataPathRecheckTask?.cancel()
            dataPathRecheckTask = nil
        }
        verifyDataPath(for: connection, isRecheck: true)
    }

    private func scheduleDegradedRecheck() {
        dataPathRecheckTask?.cancel()
        dataPathRecheckTask = nil
        guard !recheckPolicy.shouldDeferProbe, recheckPolicy.isDegraded else { return }
        guard let delay = recheckPolicy.nextDelay else {
            recordTimeline("check.recheck_exhausted")
            return
        }
        recordTimeline("check.recheck_scheduled", detail: "in \(delay)s")
        dataPathRecheckTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: delay * 1_000_000_000)
            guard !Task.isCancelled, let self else { return }
            self.dataPathRecheckTask = nil
            guard self.recheckPolicy.beginScheduledRecheck() else { return }
            self.recheckDegradedDataPath(resetBackoff: false)
        }
    }

    /// 连接离开 connected 时，补测状态随之作废；下次连上会做完整验收。
    private func resetDegradedRecheck() {
        engine?.lineProbeReadings = []
        recheckPolicy.clearDegraded()
        dataPathProbeIsRecheck = false
        dataPathProbeStage = nil
        dataPathAwaitingTailscaleLogin = false
        dataPathRecheckTask?.cancel()
        dataPathRecheckTask = nil
        engine?.dataPathRechecking = false
    }

    /// 验收没通过（或到点、或因进后台暂停）但保留隧道：首页显示已连接并提示自检未通过，
    /// 随后在前台按退避间隔自动补测。summary 为 nil 时沿用当前的未通过原因。
    private func settleDegraded(summary: String?, connection: NEVPNConnection) {
        dataPathProbeTask = nil
        dataPathProbeID = nil
        dataPathVerified = false
        dataPathProbeIsRecheck = false
        dataPathProbeStage = nil
        dataPathAwaitingTailscaleLogin = false
        activeStartAttemptID = nil
        startWatchdogTask?.cancel()
        startWatchdogTask = nil
        armSystemOnDemandIfReady()
        engine?.applySystemStatus("connected", connectedAt: connection.connectedDate)
        if let summary {
            engine?.dataPathSummary = recheckPolicy.shouldDeferProbe ? summary : dataPathDegradedPrefix + summary
        }
        engine?.dataPathRechecking = false
        recheckPolicy.markDegraded()
        scheduleDegradedRecheck()
    }

    /// 绑定引擎(若 init 时未传)。绑定后立即把当前 session 注入。
    func bind(engine: GoEngine) {
        self.engine = engine
        injectSession()
    }

    // MARK: - Load

    /// 加载系统里已存在的 XDial 隧道配置。
    /// - 找到匹配的(providerBundleIdentifier == kTunnelBundleIdentifier)就采用第一个;
    /// - 找不到则 manager 置 nil,isProfileInstalled = false(等 ensureConfigured 再建)。
    ///
    // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
    // loadAllFromPreferences 需要 NetworkExtension entitlement(Personal VPN / Packet Tunnel)。
    // 没有正确签名/描述文件时会直接回错误。真机要确认:首次(无配置)返回空数组而非报错、
    // 多个配置时的取舍、以及回调线程(这里统一切回 MainActor)。
    func loadFromPreferences(
        operationID: UUID? = nil,
        completion: (@Sendable (Result<Void, Error>) -> Void)? = nil
    ) {
        NETunnelProviderManager.loadAllFromPreferences { [weak self] managers, error in
            let box = UncheckedBox(value: (managers, error))
            Task { @MainActor in
                guard let self else { return }
                guard self.isCurrentStartOperation(operationID) else { return }
                let (managers, error) = box.value
                if let error {
                    appLog("TunnelManager.load: error \(error.localizedDescription)")
                    self.isProfileInstalled = false
                    completion?(.failure(error))
                    return
                }
                let mine = (managers ?? []).first { mgr in
                    (mgr.protocolConfiguration as? NETunnelProviderProtocol)?
                        .providerBundleIdentifier == kTunnelBundleIdentifier
                }
                self.manager = mine
                self.isProfileInstalled = (mine != nil)
                self.systemOnDemandActive = mine?.isOnDemandEnabled == true
                self.systemOnDemandState = self.systemOnDemandActive
                    ? .active
                    : (self.systemOnDemandRequested ? .pending : .disabled)
                self.restoreAcceptancePlanFromSystemProfileIfNeeded(
                    mine?.protocolConfiguration as? NETunnelProviderProtocol
                )
                let hasSecureEnvelope = self.restoreAcceptancePlanFromSecureEnvelopeIfNeeded()
                appLog("TunnelManager.load: found \((managers ?? []).count) managers, mine=\(mine != nil)")
                self.injectSession()
                if SystemOnDemandReconnectPolicy.shouldSuspendActiveProfile(
                    systemOnDemandActive: self.systemOnDemandActive,
                    userEnabled: self.systemOnDemandRequested,
                    hasStartEnvelope: hasSecureEnvelope
                ) {
                    self.suspendSystemOnDemand(clearEnvelope: true) {}
                }
                completion?(.success(()))
            }
        }
    }

    func refreshProfileStatus(completion: @escaping @Sendable (Bool) -> Void) {
        loadFromPreferences { [weak self] _ in
            Task { @MainActor in
                completion(self?.isProfileInstalled ?? false)
            }
        }
    }

    func removeProfile(completion: @escaping @Sendable (Result<Void, Error>) -> Void) {
        onDemandSaveID = nil
        pendingOnDemandEnvelope = nil
        systemOnDemandActive = false
        systemOnDemandState = systemOnDemandRequested ? .pending : .disabled
        _ = OnDemandStartEnvelopeStore.clear()
        guard let manager else {
            isProfileInstalled = false
            injectSession()
            completion(.success(()))
            return
        }
        guard manager.connection.status == .disconnected || manager.connection.status == .invalid else {
            completion(.failure(NSError(
                domain: "XDial",
                code: -20,
                userInfo: [NSLocalizedDescriptionKey: "请先断开连接再移除系统描述文件"]
            )))
            return
        }
        manager.removeFromPreferences { [weak self] error in
            let errorBox = UncheckedBox(value: error)
            Task { @MainActor in
                guard let self else { return }
                if let error = errorBox.value {
                    completion(.failure(error))
                    return
                }
                self.manager = nil
                self.isProfileInstalled = false
                self.injectSession()
                completion(.success(()))
            }
        }
    }

    func setSystemOnDemandEnabled(_ enabled: Bool) {
        systemOnDemandRequested = enabled
        if enabled {
            guard !systemOnDemandSuspendedForSetup else {
                systemOnDemandState = .pending
                return
            }
            if !systemOnDemandActive {
                systemOnDemandState = .pending
            }
            armSystemOnDemandIfReady()
        } else {
            systemOnDemandState = .disabled
            suspendSystemOnDemand(clearEnvelope: true) {}
        }
    }

    func setSystemOnDemandSuspendedForSetup(
        _ suspended: Bool,
        completion: @escaping () -> Void
    ) {
        systemOnDemandSuspendedForSetup = suspended
        if suspended {
            // 这是临时租约，不得清掉通过数据面验收后保存的安全启动包。
            let completionBox = UncheckedBox(value: completion)
            suspendSystemOnDemand(clearEnvelope: false) {
                completionBox.value()
            }
        } else {
            armSystemOnDemandIfReady()
            completion()
        }
    }

    static func makeSystemOnDemandRules() -> [NEOnDemandRule] {
        let connect = NEOnDemandRuleConnect()
        connect.interfaceTypeMatch = .any
        return [connect]
    }

    @discardableResult
    func restoreAcceptancePlanFromSystemProfileIfNeeded(
        _ providerProtocol: NETunnelProviderProtocol?
    ) -> Bool {
        if activeAcceptancePlan != nil {
            return true
        }
        guard let rawPlan = providerProtocol?.providerConfiguration?[
            Self.acceptancePlanProviderConfigurationKey
        ] as? String,
              let data = rawPlan.data(using: .utf8),
              let plan = try? JSONDecoder().decode(TunnelAcceptancePlan.self, from: data) else {
            return false
        }
        activeAcceptancePlan = plan
        return true
    }

    @discardableResult
    private func restoreAcceptancePlanFromSecureEnvelopeIfNeeded() -> Bool {
        guard case .available(let envelope) = OnDemandStartEnvelopeStore.load(),
              envelope.isValid,
              ["anyconnect", "standalone"].contains(envelope.transport),
              let rawPlan = envelope.parameters["acceptance_plan"],
              let data = rawPlan.data(using: .utf8),
              let plan = try? JSONDecoder().decode(TunnelAcceptancePlan.self, from: data) else {
            return false
        }
        if activeAcceptancePlan == nil {
            activeAcceptancePlan = plan
        }
        return true
    }

    private func armSystemOnDemandIfReady() {
        guard let manager, systemOnDemandRequested, !systemOnDemandActive, onDemandSaveID == nil,
              activeStartAllowsSystemOnDemand,
              !systemOnDemandSuspendedForSetup else { return }
        let envelope: OnDemandStartEnvelope?
        if let pendingOnDemandEnvelope {
            envelope = pendingOnDemandEnvelope
        } else if case .available(let stored) = OnDemandStartEnvelopeStore.load() {
            envelope = stored
        } else {
            envelope = nil
        }
        let tunnelReady = manager.connection.status == .connected
        guard SystemOnDemandReconnectPolicy.shouldArm(
            userEnabled: systemOnDemandRequested,
            tunnelReady: tunnelReady,
            hasStartEnvelope: envelope?.isValid == true
        ), let envelope else {
            return
        }
        let keychainStatus = OnDemandStartEnvelopeStore.save(envelope)
        guard keychainStatus == errSecSuccess else {
            systemOnDemandActive = false
            let message = "无法安全保存系统级按需重连启动包"
            systemOnDemandState = .failed(message)
            engine?.lastError = message
            recordTimeline("on_demand.failed", result: .failed, detail: "keychain=\(keychainStatus)")
            appLog("TunnelManager.onDemand: secure envelope save failed status=\(keychainStatus)")
            return
        }
        enableSystemOnDemand(manager)
    }

    private func enableSystemOnDemand(_ manager: NETunnelProviderManager) {
        let saveID = UUID()
        onDemandSaveID = saveID
        recordTimeline("on_demand.enabling")
        manager.onDemandRules = Self.makeSystemOnDemandRules()
        manager.isOnDemandEnabled = true
        manager.saveToPreferences { [weak self] error in
            let box = UncheckedBox(value: error)
            Task { @MainActor in
                guard let self, self.onDemandSaveID == saveID else { return }
                self.onDemandSaveID = nil
                if let error = box.value {
                    manager.isOnDemandEnabled = false
                    self.systemOnDemandActive = false
                    let message = "启用系统级按需重连失败：\(error.localizedDescription)"
                    self.systemOnDemandState = .failed(message)
                    self.engine?.lastError = message
                    _ = OnDemandStartEnvelopeStore.clear()
                    self.recordTimeline("on_demand.failed", result: .failed, detail: "preferences=\((error as NSError).code)")
                    appLog("TunnelManager.onDemand: enable failed \(error.localizedDescription)")
                    return
                }
                self.systemOnDemandActive = true
                self.systemOnDemandState = .active
                self.recordTimeline("on_demand.enabled", result: .ok)
                appLog("TunnelManager.onDemand: armed")
            }
        }
    }

    private func suspendSystemOnDemand(
        clearEnvelope: Bool,
        completion: @escaping @MainActor () -> Void
    ) {
        onDemandSaveID = nil
        if clearEnvelope {
            pendingOnDemandEnvelope = nil
            _ = OnDemandStartEnvelopeStore.clear()
        }
        systemOnDemandActive = false
        systemOnDemandState = systemOnDemandRequested ? .pending : .disabled
        guard let manager else {
            completion()
            return
        }
        manager.isOnDemandEnabled = false
        let completionBox = UncheckedBox(value: completion)
        manager.saveToPreferences { [weak self] error in
            let errorBox = UncheckedBox(value: error)
            Task { @MainActor in
                if let error = errorBox.value {
                    appLog("TunnelManager.onDemand: suspend failed \(error.localizedDescription)")
                    let message = "无法停用系统级按需重连：\(error.localizedDescription)"
                    self?.systemOnDemandState = .failed(message)
                    // This is a preference-save error, independent of the tunnel result.
                    #if !os(iOS)
                    self?.engine?.lastError = message
                    #endif
                }
                completionBox.value()
            }
        }
    }

    // MARK: - Configure & Save

    private static let acceptancePlanProviderConfigurationKey = "acceptancePlan"

    static func systemProviderConfiguration(
        acceptancePlan: TunnelAcceptancePlan?
    ) -> [String: Any] {
        var configuration: [String: Any] = [
            "appGroup": kAppGroupIdentifier,
            "schemaVersion": 1,
        ]
        if let acceptancePlan,
           let data = try? JSONEncoder().encode(acceptancePlan),
           let rawPlan = String(data: data, encoding: .utf8) {
            configuration[acceptancePlanProviderConfigurationKey] = rawPlan
        }
        return configuration
    }

    /// 确保系统里有一份「与 profile 对应」的隧道配置:
    /// 已有则更新其 protocolConfiguration,没有则新建一个 manager,然后 saveToPreferences。
    /// serverAddress 从 profile 的活动线路默认出口推导(仅用于系统 UI 展示,真实拨号参数
    /// 由 startTunnel(options:) 一次性交给扩展)。
    ///
    // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
    // saveToPreferences 是触发用户授权弹窗的地方(首次会弹「XDial 想要添加 VPN 配置」)。
    // 授权被拒 / 描述文件缺 NetworkExtension 能力 / bundle id 不匹配都会在这里失败。
    // 真机要确认:弹窗时机、拒绝后的 error code、重复 save 是否幂等、save 后 connection 是否可用。
    func ensureConfigured(
        serverAddress: String,
        operationID: UUID? = nil,
        completion: (@Sendable (Result<Void, Error>) -> Void)? = nil
    ) {
        guard isCurrentStartOperation(operationID) else { return }
        let mgr = manager ?? NETunnelProviderManager()

        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = kTunnelBundleIdentifier
        // serverAddress 不能为空,否则系统会拒绝保存;空则用占位。
        proto.serverAddress = Self.systemProfileServerAddress(serverAddress)
        // providerConfiguration 只放最基本的 key。完整配置不塞这里，
        // 运行时通过一次性 start options 下发；这里只放扩展身份所需的最小元信息。
        // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
        // providerConfiguration 有大小与类型限制(只接受 plist 可序列化类型),
        // 且系统对其可见，不应放密码等敏感数据（敏感启动参数只走 start options）。
        proto.providerConfiguration = Self.systemProviderConfiguration(
            acceptancePlan: activeAcceptancePlan
        )

        mgr.protocolConfiguration = proto
        mgr.localizedDescription = kTunnelLocalizedDescription
        mgr.isEnabled = true
        // 手动启动期间先暂停系统接管，避免保存配置与携带一次性 options 的
        // startVPNTunnel 之间被系统抢先冷启动。
        mgr.onDemandRules = Self.makeSystemOnDemandRules()
        mgr.isOnDemandEnabled = false
        systemOnDemandActive = false
        systemOnDemandState = systemOnDemandRequested ? .pending : .disabled

        let mgrBox = UncheckedBox(value: mgr)
        mgr.saveToPreferences { [weak self] error in
            let box = UncheckedBox(value: error)
            Task { @MainActor in
                guard let self else { return }
                guard self.isCurrentStartOperation(operationID) else { return }
                if let error = box.value {
                    appLog("TunnelManager.save: error \(error.localizedDescription)")
                    completion?(.failure(error))
                    return
                }
                // saveToPreferences 后,系统给回的对象可能需要重新 load 一次才「完整」
                // (Apple 文档惯例:save 后紧接 load 以拿到系统规范化后的配置)。
                // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
                self.manager = mgrBox.value
                self.isProfileInstalled = true
                appLog("TunnelManager.save: ok")
                self.loadFromPreferences(operationID: operationID) { result in
                    completion?(result)
                }
            }
        }
    }

    private func isCurrentStartOperation(_ operationID: UUID?) -> Bool {
        guard let operationID else { return true }
        return startOperationState.isCurrent(operationID)
    }

    // MARK: - Session injection

    /// 从当前 manager 取真实会话,重建 TunnelProviderSession 并注入 GoEngine.session。
    /// manager 为 nil 或 connection 不是 NETunnelProviderSession 时,注入一个空会话
    /// (sendProviderMessage 会抛错),语义等价「隧道不可用」。
    private func injectSession() {
        let real = manager?.connection as? NETunnelProviderSession
        let wrapped = TunnelProviderSession(session: real)
        self.providerSession = wrapped
        engine?.session = wrapped
        appLog("TunnelManager.injectSession: real=\(real != nil), engineBound=\(engine != nil)")
        applyConnectionStatus()
    }

    private func applyConnectionStatus() {
        guard let connection = manager?.connection else {
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            observedSystemStatus = .invalid
            reportEarlyStartFailureIfNeeded()
            engine?.applySystemStatus("disconnected")
            return
        }
        let previousStatus = observedSystemStatus
        observedSystemStatus = connection.status
        if previousStatus != connection.status {
            recordTimeline("status." + Self.timelineStatusName(connection.status))
            if connection.status == .connected, let date = connection.connectedDate {
                recordTimeline("connection_established", at: Int64((date.timeIntervalSince1970 * 1000).rounded()))
            }
        }
        switch connection.status {
        case .connected:
            armSystemOnDemandIfReady()
            if dataPathVerified {
                engine?.applySystemStatus("connected", connectedAt: connection.connectedDate)
            } else if previousStatus == .connected && recheckPolicy.isDegraded {
                // 连接一直在，只是又收到一次状态通知：按补测处理，不把首页拉回「检查中」。
                recheckDegradedDataPath(resetBackoff: false)
            } else if previousStatus != .connected || dataPathProbeTask == nil {
                verifyDataPath(for: connection)
            }
        case .connecting:
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            engine?.applySystemStatus("connecting")
        case .disconnecting:
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            engine?.applySystemStatus("disconnecting")
        case .reasserting:
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            engine?.applySystemStatus("reconnecting", connectedAt: connection.connectedDate)
        case .disconnected, .invalid:
            #if os(iOS)
            if connection.status == .disconnected,
               previousStatus == .connected || previousStatus == .reasserting || previousStatus == .disconnecting {
                // A killed provider cannot run either of its notification paths.
                // iOS distinguishes this case from a manual stop and provider-reported errors.
                connection.fetchLastDisconnectError { error in
                    guard let error = error as NSError?, error.domain == NEVPNConnectionErrorDomain,
                          error.code == NEVPNConnectionError.pluginFailed.rawValue else { return }
                    ConnectionNotice.disconnect.post()
                }
            }
            #endif
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            // The provider revokes the secure start on terminal failures, even
            // while the app is suspended. Retire the matching system rule too.
            if (systemOnDemandActive || onDemandSaveID != nil),
               case .missing = OnDemandStartEnvelopeStore.load() {
                engine?.suppressNextAutomaticReconnect()
                suspendSystemOnDemand(clearEnvelope: true) {}
            } else if onDemandSaveID != nil {
                engine?.suppressNextAutomaticReconnect()
            }
            reportEarlyStartFailureIfNeeded()
            engine?.applySystemStatus("disconnected")
            if previousStatus != connection.status, previousStatus != .invalid {
                timelineSessionID = nil
            }
        @unknown default:
            dataPathProbeTask?.cancel()
            dataPathProbeTask = nil
            dataPathProbeID = nil
            dataPathVerified = false
            resetDegradedRecheck()
            reportEarlyStartFailureIfNeeded()
            engine?.applySystemStatus("disconnected")
        }
    }

    static func timelineStatusName(_ status: NEVPNStatus) -> String {
        switch status {
        case .invalid: return "invalid"
        case .disconnected: return "disconnected"
        case .connecting: return "connecting"
        case .connected: return "connected"
        case .reasserting: return "reasserting"
        case .disconnecting: return "disconnecting"
        @unknown default: return "unknown"
        }
    }

    /// 系统状态 connected 只是控制面信号。只有主 App 的真实 HTTPS 请求通过后，
    /// 才允许引擎/UI 进入 connected；失败会保留具体层级错误并立即断开，避免手机
    /// 留在“图标已连但无法上网”的坏状态。
    ///
    /// isRecheck 用于自检没通过、连接已保留后的补测：首页保持「已连接」并显示检测中，
    /// 通过就收起提示，没通过就更新原因、继续按退避补测。
    private func verifyDataPath(for connection: NEVPNConnection, isRecheck: Bool = false) {
        dataPathProbeTask?.cancel()
        // 控制面已经 connected，从这里开始给数据面验收一个独立时限。
        // 不能继续消耗 AnyConnect 握手前就开始的 45 秒，否则慢网会在
        // 线路已通、正验收分流时被误判为启动超时。
        startWatchdogTask?.cancel()
        // 系统可能在 App 已经进入后台后才连上；这条路径没有新的后台通知。
        // 首次验收、手动补测、重复 connected 通知统一在此延期，不启动探针或看门狗。
        if recheckPolicy.shouldDeferProbe {
            recordTimeline("check.deferred_background", result: .notRun, detail: isRecheck ? "recheck" : "first")
            settleDegraded(
                summary: isRecheck ? nil : "App 在后台，自检暂停；回到前台会自动重新检测",
                connection: connection
            )
            return
        }
        // 任何入口开始验收都要收回旧补测计时，避免运行中耗掉下一次预算。
        dataPathRecheckTask?.cancel()
        dataPathRecheckTask = nil
        let probeID = UUID()
        dataPathProbeID = probeID
        probeStartedAt = ProcessInfo.processInfo.systemUptime
        checkStartedAt = probeStartedAt
        recordTimeline("check.begin", detail: isRecheck ? "recheck" : "first")
        probeCheckpoints.removeAll()
        dataPathProbeIsRecheck = isRecheck
        dataPathProbeStage = nil
        dataPathAwaitingTailscaleLogin = false
        let attemptID = activeStartAttemptID
        let acceptancePlan = activeAcceptancePlan
        dataPathVerified = false
        if !isRecheck { engine?.lineProbeReadings = [] }
        if isRecheck {
            engine?.dataPathRechecking = true
            appLog("DataPathProbe: recheck begin")
        } else {
            recheckPolicy.clearDegraded()
            engine?.dataPathRechecking = false
            engine?.dataPathSummary = acceptancePlan?.requiresAnyConnect == true
                ? "正在检测 Direct、AnyConnect 与分流规则"
                : "正在检测活动线路、域名解析与分流规则"
            engine?.applySystemStatus("connected", connectedAt: connection.connectedDate)
            appLog("DataPathProbe: begin")
        }

        armDataPathWatchdog(
            probeID: probeID,
            attemptID: attemptID,
            connection: connection
        )

        dataPathProbeTask = Task { [weak self, weak connection] in
            guard let self else { return }
            let outcome: ProbeOutcome
            if let acceptancePlan {
                switch await self.waitForActiveTailscaleReadiness(
                    plan: acceptancePlan,
                    probeID: probeID,
                    attemptID: attemptID,
                    connection: connection
                ) {
                case .ready:
                    outcome = await self.probeConfiguredDataPath(plan: acceptancePlan)
                case .failed(let message):
                    outcome = ProbeOutcome(
                        isUsable: false,
                        summary: message,
                        failureMessage: "\(message)，已自动断开",
                        logSummary: "tailscale-readiness-failed"
                    )
                }
            } else {
                outcome = ProbeOutcome(
                    isUsable: false,
                    summary: "连接验收计划不可用",
                    failureMessage: "连接验收计划不可用，已自动断开",
                    logSummary: "acceptance-plan-missing"
                )
            }
            guard !Task.isCancelled, let connection,
                  self.dataPathProbeID == probeID,
                  connection === self.manager?.connection,
                  connection.status == .connected else { return }

            self.dataPathProbeTask = nil
            if outcome.isUsable {
                self.engine?.dataPathSummary = outcome.summary
                self.dataPathProbeID = nil
                self.dataPathVerified = true
                self.engine?.dataPathDidPass.send()
                self.resetDegradedRecheck()
                self.activeStartAttemptID = nil
                self.startWatchdogTask?.cancel()
                self.startWatchdogTask = nil
                self.armSystemOnDemandIfReady()
                self.engine?.applySystemStatus("connected", connectedAt: connection.connectedDate)
                appLog("DataPathProbe: passed \(outcome.logSummary)")
                self.recordTimeline("check.passed", result: .ok,
                                    ms: ConnectionTimeline.elapsedMilliseconds(since: self.checkStartedAt))
            } else if Self.keepsTunnelWhenAcceptanceFails || self.dataPathProbeIsRecheck {
                // 机上 Wi-Fi、弱信号等高延迟或受限网络里，验收探针常常超时，但隧道本身已经在转发
                // 流量。主动断开会把能用的连接断掉，所以保留连接，只记下未通过的原因供首页提示。
                appLog("DataPathProbe: degraded, keeping tunnel \(outcome.logSummary)")
                self.recordTimeline("check.degraded", result: .failed,
                                    ms: ConnectionTimeline.elapsedMilliseconds(since: self.checkStartedAt),
                                    detail: outcome.timelineDetail)
                self.settleDegraded(summary: outcome.summary, connection: connection)
            } else {
                self.engine?.dataPathSummary = outcome.summary
                self.dataPathVerified = false
                appLog("DataPathProbe: failed \(outcome.logSummary)")
                self.recordTimeline("check.failed", result: .failed,
                                    ms: ConnectionTimeline.elapsedMilliseconds(since: self.checkStartedAt),
                                    detail: outcome.timelineDetail)
                let baseMessage = outcome.failureMessage
                self.applyProbeFailure(
                    baseMessage: baseMessage,
                    diagnostics: TunnelDiagnosticFormatter.persistedSummary(
                        matchingAttemptID: attemptID
                    ),
                    connection: connection,
                    shouldStop: false
                )

                let fallbackStop = Task { @MainActor [weak self, weak connection] in
                    try? await Task.sleep(nanoseconds: 800_000_000)
                    guard !Task.isCancelled, let self, let connection,
                          self.dataPathProbeID == probeID else { return }
                    self.applyProbeFailure(
                        baseMessage: baseMessage,
                        diagnostics: TunnelDiagnosticFormatter.persistedSummary(
                            matchingAttemptID: attemptID
                        ),
                        connection: connection,
                        shouldStop: true
                    )
                }
                self.requestProviderDiagnostics(
                    matchingAttemptID: attemptID
                ) { [weak self, weak connection] diagnostics in
                    fallbackStop.cancel()
                    guard let self, let connection,
                          self.dataPathProbeID == probeID else { return }
                    self.applyProbeFailure(
                        baseMessage: baseMessage,
                        diagnostics: diagnostics ?? TunnelDiagnosticFormatter.persistedSummary(
                            matchingAttemptID: attemptID
                        ),
                        connection: connection,
                        shouldStop: true
                    )
                }
            }
        }
    }

    private func armDataPathWatchdog(
        probeID: UUID,
        attemptID: String?,
        connection: NEVPNConnection
    ) {
        startWatchdogTask?.cancel()
        startWatchdogTask = Task { @MainActor [weak self, weak connection] in
            try? await Task.sleep(nanoseconds: Self.dataPathWatchdogSeconds * 1_000_000_000)
            guard !Task.isCancelled, let self, let connection,
                  self.dataPathProbeID == probeID else { return }
            self.dataPathProbeTask?.cancel()
            self.dataPathProbeTask = nil
            // 只写「超时」看不出原因：带上到点时停在哪一步。
            self.recordProbeCheckpoint("本轮验收计时到期（限时 \(Self.dataPathWatchdogSeconds)s）")
            let timeline = self.probeCheckpoints.joined(separator: " → ")
            let stageSummary = self.dataPathProbeStage.map { "连接验收超时（停在：\($0)）" }
                ?? "连接验收超时"
            let timeoutSummary = stageSummary + "；验收经过：" + timeline
            self.recordTimeline("check.watchdog_expired", result: .timeout,
                                ms: ConnectionTimeline.elapsedMilliseconds(since: self.checkStartedAt),
                                detail: self.dataPathProbeStage)
            if (Self.keepsTunnelWhenAcceptanceFails || self.dataPathProbeIsRecheck),
               connection.status == .connected {
                appLog("DataPathProbe: watchdog expired, keeping tunnel: \(timeoutSummary)")
                self.settleDegraded(summary: timeoutSummary, connection: connection)
                return
            }
            self.applyProbeFailure(
                baseMessage: timeoutSummary,
                diagnostics: TunnelDiagnosticFormatter.persistedSummary(
                    matchingAttemptID: attemptID
                ),
                connection: connection,
                shouldStop: true
            )
        }
    }

    private enum TailscaleReadiness {
        case ready
        case failed(String)
    }

    private enum TailscaleStatusFetchResult: Sendable {
        case success(TailscaleRuntimeStatus)
        case failure(String)
        case cancelled
    }

    /// tsnet endpoint 在首次启动时必须先保持 provider 存活，才能暴露 LocalAPI
    /// authURL。这里在任何“线路不通”探针之前识别 NeedsLogin，进入明确的等待授权
    /// 状态；授权完成后重新从完整数据面验收起点开始。
    private func waitForActiveTailscaleReadiness(
        plan: TunnelAcceptancePlan,
        probeID: UUID,
        attemptID: String?,
        connection: NEVPNConnection?
    ) async -> TailscaleReadiness {
        let tags = plan.generatedTailscaleTargets.map(\.tag)
        guard !tags.isEmpty else { return .ready }
        dataPathProbeStage = "等待 Tailscale 启动"

        var enteredActionRequired = false
        var transientPolls = 0
        var consecutiveStatusFailures = 0
        var lastStatusObservations: [String: String] = [:]
        var loginRequests: Set<String> = []
        while !Task.isCancelled {
            guard dataPathProbeID == probeID,
                  let connection,
                  connection === manager?.connection,
                  connection.status == .connected else {
                return .failed("Tailscale 登录等待已取消")
            }

            var allRunning = true
            var needsLogin = false
            var sawTransient = false
            var statusRequestFailed = false
            for tag in tags {
                let result = await requestTailscaleRuntimeStatus(endpointTag: tag)
                switch result {
                case .failure(let message):
                    allRunning = false
                    sawTransient = true
                    statusRequestFailed = true
                    lastStatusObservations[tag] = "读取失败：\(message)"
                case .cancelled:
                    return .failed("Tailscale 登录等待已取消")
                case .success(let status):
                    lastStatusObservations[tag] = status.backendState.isEmpty
                        ? "空状态"
                        : status.backendState
                    switch status.backendState.lowercased() {
                    case "running":
                        continue
                    case "needslogin", "needs_login",
                         "needsmachineauth", "needs_machine_auth":
                        allRunning = false
                        if Self.isValidTailscaleAuthURL(status.authURL) {
                            needsLogin = true
                        } else if loginRequests.insert(tag).inserted {
                            let loginResult = await requestTailscaleRuntimeStatus(
                                command: "tailscale-begin-login",
                                endpointTag: tag
                            )
                            switch loginResult {
                            case .failure(let message):
                                lastStatusObservations[tag] = "登录初始化失败：\(message)"
                                sawTransient = true
                            case .cancelled:
                                return .failed("Tailscale 登录等待已取消")
                            case .success(let loginStatus):
                                lastStatusObservations[tag] = loginStatus.backendState.isEmpty
                                    ? "空状态"
                                    : loginStatus.backendState
                                if Self.isValidTailscaleAuthURL(loginStatus.authURL) {
                                    needsLogin = true
                                } else {
                                    sawTransient = true
                                }
                            }
                        } else {
                            sawTransient = true
                        }
                    case "starting", "stopped", "nostate", "no_state":
                        allRunning = false
                        sawTransient = true
                    default:
                        allRunning = false
                        if enteredActionRequired {
                            sawTransient = true
                        } else {
                            return .failed("Tailscale 状态异常：\(status.backendState)")
                        }
                    }
                }
            }
            consecutiveStatusFailures = statusRequestFailed
                ? consecutiveStatusFailures + 1
                : 0
            if enteredActionRequired && consecutiveStatusFailures >= 5 {
                engine?.dataPathSummary = "持续无法读取 Tailscale 登录状态"
                return .failed("持续无法读取 Tailscale 登录状态")
            }

            if allRunning {
                dataPathAwaitingTailscaleLogin = false
                if enteredActionRequired {
                    engine?.dataPathSummary = "Tailscale 已登录，正在重新执行完整连接验收"
                    engine?.applySystemStatus("connected", connectedAt: connection.connectedDate)
                    armDataPathWatchdog(
                        probeID: probeID,
                        attemptID: attemptID,
                        connection: connection
                    )
                }
                return .ready
            }

            if needsLogin {
                enteredActionRequired = true
                dataPathAwaitingTailscaleLogin = true
                dataPathProbeStage = "等待登录 Tailscale"
                transientPolls = 0
                startWatchdogTask?.cancel()
                startWatchdogTask = nil
                engine?.lastError = nil
                engine?.dataPathSummary = "Tailscale 需要登录；隧道保持运行，登录后将自动重新验收"
                engine?.applySystemStatus(
                    "action-required",
                    connectedAt: connection.connectedDate
                )
                try? await Task.sleep(nanoseconds: 2_000_000_000)
                continue
            }

            if sawTransient {
                transientPolls += 1
                if !enteredActionRequired && transientPolls >= 30 {
                    let observations = lastStatusObservations
                        .sorted { $0.key < $1.key }
                        .map { "\($0.key)=\($0.value)" }
                        .joined(separator: "；")
                    let detail = observations.isEmpty ? "没有状态响应" : observations
                    engine?.dataPathSummary = "Tailscale 启动状态：\(detail)"
                    return .failed("Tailscale 未能进入可登录或已运行状态（\(detail)）")
                }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                continue
            }
        }
        return .failed("Tailscale 登录等待已取消")
    }

    private static func isValidTailscaleAuthURL(_ rawValue: String) -> Bool {
        guard let components = URLComponents(string: rawValue),
              components.scheme?.lowercased() == "https",
              let host = components.host, !host.isEmpty else { return false }
        return true
    }

    private func requestTailscaleRuntimeStatus(
        command: String = "tailscale-status",
        endpointTag: String
    ) async -> TailscaleStatusFetchResult {
        guard let session = providerSession,
              let request = try? JSONSerialization.data(withJSONObject: [
                  "cmd": command,
                  "endpoint_tag": endpointTag,
              ]) else {
            return .failure("Tailscale 状态接口不可用")
        }
        let oneShot = OneShotContinuation<TailscaleStatusFetchResult>()
        return await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                oneShot.install(continuation)
                DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 3) {
                    oneShot.finish(.failure("读取 Tailscale 状态超时"))
                }
                do {
                    try session.sendProviderMessage(request) { responseData in
                        guard let responseData,
                              let response = try? JSONDecoder().decode(
                                  DaemonResponse.self,
                                  from: responseData
                              ),
                              response.ok == true,
                              let rawStatus = response.data,
                              let data = rawStatus.data(using: .utf8),
                              let status = try? JSONDecoder().decode(
                                  TailscaleRuntimeStatus.self,
                                  from: data
                              ) else {
                            oneShot.finish(.failure("Tailscale 状态响应无效"))
                            return
                        }
                        oneShot.finish(.success(status))
                    }
                } catch {
                    oneShot.finish(.failure(error.localizedDescription))
                }
            }
        } onCancel: {
            oneShot.finish(.cancelled)
        }
    }

    private struct ProbeOutcome {
        let isUsable: Bool
        let summary: String
        let failureMessage: String
        let logSummary: String
        /// Short, label-free verdict for the connection timeline.
        var timelineDetail: String? = nil

        static let cancelled = ProbeOutcome(
            isUsable: false, summary: "", failureMessage: "", logSummary: "cancelled"
        )
    }

    private struct OutboundAddressProbeResult: Sendable {
        let address: String?
        let failure: String?
        var ms: Int = 0
    }

    private func probeLines(_ targets: [TunnelAcceptanceTarget]) async -> [String: OutboundAddressProbeResult] {
        let probeID = dataPathProbeID
        recordProbeCheckpoint("开始线路试拨")
        var results: [String: OutboundAddressProbeResult] = [:]
        await withTaskGroup(of: (TunnelAcceptanceTarget, OutboundAddressProbeResult).self) { group in
            for target in targets {
                group.addTask { [weak self] in
                    guard let self else {
                        return (target, OutboundAddressProbeResult(address: nil, failure: "线路探针不可用"))
                    }
                    let startedAt = ProcessInfo.processInfo.systemUptime
                    var probe = await self.requestOutboundAddress(outboundTag: target.tag)
                    probe.ms = ConnectionTimeline.elapsedMilliseconds(since: startedAt)
                    return (target, probe)
                }
            }
            for await (target, probe) in group {
                guard !Task.isCancelled, probeID != nil, probeID == dataPathProbeID else { continue }
                results[target.tag] = probe
                engine?.lineProbeReadings = targets.compactMap { target in
                    guard let reading = results[target.tag] else { return nil }
                    return LineProbeReading(label: target.label, result: reading.address != nil
                        ? .ok : ConnectionTimeline.classify(probeFailure: reading.failure))
                }
                recordTimeline(
                    "check.line",
                    result: probe.address != nil ? .ok
                        : ConnectionTimeline.classify(probeFailure: probe.failure),
                    ms: probe.ms,
                    detail: target.label + (probe.failure.map { " " + ConnectionTimeline.failureKeywords($0) } ?? ""))
            }
        }
        return results
    }

    private func probeConfiguredDataPath(plan: TunnelAcceptancePlan) async -> ProbeOutcome {
        var addresses: [String: String] = [:]
        var unavailable: [TunnelAcceptanceTarget] = []
        var probeFailures: [String] = []
        // 指定出口试拨绕过路由匹配，可与计数快照之间的系统请求并行。
        async let lineResults = probeLines(plan.targets)
        dataPathProbeStage = "读取测试前分流命中计数"
        let baselineStartedAt = ProcessInfo.processInfo.systemUptime
        let baseline = await requestRoutingProbeSnapshot()
        guard !Task.isCancelled else {
            return .cancelled
        }
        recordTimeline("check.route_counter_before", result: baseline.snapshot == nil ? .failed : .ok,
                       ms: ConnectionTimeline.elapsedMilliseconds(since: baselineStartedAt),
                       detail: baseline.snapshot == nil ? baseline.detail : nil)
        recordProbeCheckpoint("测试前计数：" + baseline.detail)
        dataPathProbeStage = "测试分流与域名解析"
        let routed = await DataPathProbe.run(
            directLineEgressIP: nil,
            anyConnectLineEgressIP: nil,
            includeSplitTarget: plan.currentRouteTag != nil
        )
        guard !Task.isCancelled else {
            return .cancelled
        }
        recordTimeline("check.direct_request", result: routed.directCheck.result,
                       ms: routed.directCheck.ms, detail: "cf-trace via Direct" + routed.directCheck.errorDetail, at: routed.directCheck.completedAt)
        if plan.currentRouteTag != nil {
            recordTimeline("check.current_request", result: routed.currentCheck.result,
                           ms: routed.currentCheck.ms, detail: "cf-trace via current line" + routed.currentCheck.errorDetail, at: routed.currentCheck.completedAt)
        }
        recordTimeline("check.hostname_request", result: routed.hostnameCheck.result,
                       ms: routed.hostnameCheck.ms, detail: "www.apple.com" + routed.hostnameCheck.errorDetail, at: routed.hostnameCheck.completedAt)
        let direct = routed.routedDirectEgressIP == nil ? "未取得出口" : "取得出口"
        let current = plan.currentRouteTag == nil ? "跳过"
            : (routed.routedAnyConnectEgressIP == nil ? "未取得出口" : "取得出口")
        recordProbeCheckpoint("直连测试 \(direct)，当前线路测试 \(current)，域名请求\(routed.hostnameOK ? "成功" : "失败")")
        dataPathProbeStage = "读取测试后分流命中计数"
        let finalStartedAt = ProcessInfo.processInfo.systemUptime
        let final = await requestRoutingProbeSnapshot()
        guard !Task.isCancelled else { return .cancelled }
        recordTimeline("check.route_counter_after", result: final.snapshot == nil ? .failed : .ok,
                       ms: ConnectionTimeline.elapsedMilliseconds(since: finalStartedAt),
                       detail: final.snapshot == nil ? final.detail : nil)
        let routingBefore = baseline.snapshot
        let routingAfter = final.snapshot
        let routingEvidenceOK = routingBefore.flatMap { before in
            routingAfter.map {
                if let currentRouteTag = plan.currentRouteTag {
                    return before.provesConfiguredRouting(
                        after: $0,
                        directTag: "direct",
                        currentRouteTag: currentRouteTag
                    )
                }
                return before.provesConfiguredDirectRouting(
                    after: $0,
                    directTag: "direct"
                )
            }
        } ?? false
        var alternateSummary = ""
        if routed.directCheck.result != .ok {
            dataPathProbeStage = "复核直连 HTTPS"
            let alternate = await DataPathProbe.alternateHTTPSCheck()
            guard !Task.isCancelled else { return .cancelled }
            let afterAlternate = await requestRoutingProbeSnapshot()
            guard !Task.isCancelled else { return .cancelled }
            let route = routingAfter.flatMap { before in
                afterAlternate.snapshot.map { before.alternateHTTPSRoute(after: $0) }
            } ?? "unknown"
            recordTimeline("check.alternate_https", result: alternate.result, ms: alternate.ms,
                           detail: "alternate IP route=\(route)" + alternate.errorDetail, at: alternate.completedAt)
            alternateSummary = "；Direct 主探针 \(routed.directCheck.result.rawValue)\(routed.directCheck.errorDetail)"
                + "，备用 HTTPS \(alternate.result.rawValue)\(alternate.errorDetail)（路由 \(route)）"
        }
        dataPathProbeStage = "等待线路试拨"
        let results = await lineResults
        guard !Task.isCancelled else { return .cancelled }
        for target in plan.targets {
            let probe = results[target.tag]
            if let address = probe?.address {
                addresses[target.tag] = address
            } else {
                unavailable.append(target)
                if let failure = probe?.failure {
                    probeFailures.append("\(target.label)：\(failure)")
                }
            }
        }
        recordProbeCheckpoint("线路试拨成功 \(addresses.count)/\(plan.targets.count)")
        let result = ConfiguredDataPathResult(
            targetAddresses: addresses,
            targets: plan.targets,
            unavailableTargets: unavailable,
            routedDirectEgressIP: routed.routedDirectEgressIP,
            routedCurrentEgressIP: routed.routedAnyConnectEgressIP,
            hostnameOK: routed.hostnameOK,
            routingEvidenceOK: routingEvidenceOK,
            currentRouteTag: plan.currentRouteTag
        )
        var failureDetails = probeFailures.isEmpty
            ? ""
            : "；拨号错误：\(probeFailures.joined(separator: "；"))"
        if !routingEvidenceOK {
            // 「路由命中未通过」时把依据写出来：两个测试地址各被记到几次、走的哪条线路，
            // 以及测试请求有没有收到回应。否则只凭「未通过」三个字分不出是没记到还是走错了。
            recordProbeCheckpoint("测试后计数：" + final.detail)
            failureDetails += "；验收经过：" + probeCheckpoints.joined(separator: " → ")
            failureDetails += routingHitDetail(
                before: routingBefore,
                after: routingAfter,
                labels: Dictionary(plan.targets.map { ($0.tag, $0.label) }, uniquingKeysWith: { first, _ in first }),
                directReply: routed.routedDirectEgressIP,
                currentReply: plan.currentRouteTag == nil ? nil : .some(routed.routedAnyConnectEgressIP)
            )
        }
        let timelineDetail = "lines \(addresses.count)/\(plan.targets.count)"
            + " route=\(result.splitRoutingState.rawValue) evidence=\(routingEvidenceOK)"
            + " hostname=\(routed.hostnameCheck.result.rawValue)"
            + " direct=\(routed.directCheck.result.rawValue)"
            + (plan.currentRouteTag == nil ? "" : " current=\(routed.currentCheck.result.rawValue)")
        return ProbeOutcome(
            isUsable: result.isUsable,
            summary: result.diagnosticSummary + alternateSummary + failureDetails,
            failureMessage: result.failureMessage,
            logSummary: "targets=\(addresses.count)/\(plan.targets.count) "
                + "route=\(plan.currentRouteTag ?? "not-applicable") "
                + "split=\(result.splitRoutingState.rawValue) routeEvidence=\(routingEvidenceOK) "
                + "hostname=\(routed.hostnameOK) routedDirect="
                + "\(routed.routedDirectEgressIP ?? "unavailable") routedCurrent="
                + "\(routed.routedAnyConnectEgressIP ?? "unavailable")"
                + failureDetails,
            timelineDetail: timelineDetail
        )
    }

    /// 直接让 libbox 通过指定 outbound 访问出口地址，这一层绕过 route matcher，
    /// 只回答“这条线路本身通不通”。后续主 App 的 URLSession 探针才回答
    /// “配置里的分流规则有没有把系统流量送到它”。
    private func requestOutboundAddress(outboundTag: String) async -> OutboundAddressProbeResult {
        guard let session = providerSession,
              let request = try? JSONSerialization.data(withJSONObject: [
                "cmd": "probe-outbound-address",
                "outbound_tag": outboundTag,
                "timeout_ms": "10000",
              ]) else {
            return OutboundAddressProbeResult(address: nil, failure: "线路探针不可用")
        }

        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<OutboundAddressProbeResult>(continuation)
            // provider 并行执行线路探针，单条最多 10s；多留几秒给进程间消息往返。
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 15) {
                oneShot.finish(OutboundAddressProbeResult(address: nil, failure: "线路探针超时"))
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    guard let responseData,
                          let response = try? JSONDecoder().decode(
                            DaemonResponse.self,
                            from: responseData
                          ) else {
                        oneShot.finish(OutboundAddressProbeResult(
                            address: nil,
                            failure: "线路探针响应无效"
                        ))
                        return
                    }
                    guard response.ok == true,
                          let address = response.data,
                          !address.isEmpty,
                          IPv4Address(address) != nil || IPv6Address(address) != nil else {
                        oneShot.finish(OutboundAddressProbeResult(
                            address: nil,
                            failure: response.message ?? "线路探针失败"
                        ))
                        return
                    }
                    oneShot.finish(OutboundAddressProbeResult(address: address, failure: nil))
                }
            } catch {
                oneShot.finish(OutboundAddressProbeResult(
                    address: nil,
                    failure: error.localizedDescription
                ))
            }
        }
    }

    func trafficTotals() async -> TrafficTotals? {
        guard let session = providerSession else { return nil }
        let request = Data("{\"cmd\":\"traffic-snapshot\"}".utf8)
        let raw: String? = await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<String?>(continuation)
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 2) {
                oneShot.finish(nil)
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    guard let responseData,
                          let response = try? JSONDecoder().decode(DaemonResponse.self, from: responseData),
                          response.ok == true else {
                        oneShot.finish(nil)
                        return
                    }
                    oneShot.finish(response.data)
                }
            } catch {
                oneShot.finish(nil)
            }
        }
        guard let raw, let data = raw.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(TrafficTotals.self, from: data)
    }

    func directExitCountry() async -> ExitCountryProbeResult {
        guard let session = providerSession else {
            return ExitCountryProbeResult(code: nil, failure: "VPN 扩展不可用")
        }
        let request = Data("{\"cmd\":\"probe-exit-country\",\"timeout_ms\":\"8000\"}".utf8)
        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<ExitCountryProbeResult>(continuation)
            // 扩展内最多用 8 秒，留出排队的余量。
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 12) {
                oneShot.finish(ExitCountryProbeResult(code: nil, failure: "VPN 扩展没有及时回应"))
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    guard let responseData,
                          let response = try? JSONDecoder().decode(DaemonResponse.self, from: responseData) else {
                        oneShot.finish(ExitCountryProbeResult(code: nil, failure: "VPN 扩展回应无效"))
                        return
                    }
                    guard response.ok == true, let code = response.data,
                          code.count == 2, code.allSatisfy({ $0.isASCII && $0.isUppercase }) else {
                        oneShot.finish(ExitCountryProbeResult(code: nil, failure: response.message ?? "查询失败"))
                        return
                    }
                    oneShot.finish(ExitCountryProbeResult(code: code, failure: nil))
                }
            } catch {
                oneShot.finish(ExitCountryProbeResult(code: nil, failure: error.localizedDescription))
            }
        }
    }

    enum SnapshotRead: Sendable {
        case success(RoutingProbeSnapshot)
        case failure(String)

        var snapshot: RoutingProbeSnapshot? {
            if case let .success(value) = self { return value }
            return nil
        }

        var detail: String {
            if case let .failure(reason) = self { return reason }
            return "已读取"
        }

        static func decode(_ data: Data?) -> Self {
            guard let data else { return .failure("扩展返回空回包") }
            guard let response = try? JSONDecoder().decode(DaemonResponse.self, from: data)
            else { return .failure("扩展回包格式异常") }
            guard let ok = response.ok else { return .failure("扩展回包缺少状态") }
            guard ok else { return .failure("扩展报告读取失败") }
            guard let raw = response.data,
                  let snapshot = try? JSONDecoder().decode(RoutingProbeSnapshot.self, from: Data(raw.utf8))
            else { return .failure("计数数据格式异常") }
            return .success(snapshot)
        }
    }

    private func requestRoutingProbeSnapshot() async -> SnapshotRead {
        guard let session = providerSession else { return .failure("扩展会话不可用") }
        let request = Data("{\"cmd\":\"routing-probe-snapshot\"}".utf8)
        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<SnapshotRead>(continuation)
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 3) {
                oneShot.finish(.failure("3 秒内未收到计数回包"))
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    oneShot.finish(.decode(responseData))
                }
            } catch {
                oneShot.finish(.failure("计数请求发送失败"))
            }
        }
    }

    private func requestProviderDiagnostics(
        matchingAttemptID expectedAttemptID: String?,
        completion: @escaping @MainActor (String?) -> Void
    ) {
        guard let session = providerSession else {
            completion(nil)
            return
        }
        let request = Data("{\"cmd\":\"status\"}".utf8)
        let completionBox = UncheckedBox(value: completion)
        do {
            try session.sendProviderMessage(request) { responseData in
                let responseBox = UncheckedBox(value: responseData)
                Task { @MainActor in
                    completionBox.value(TunnelDiagnosticFormatter.providerSummary(
                        from: responseBox.value,
                        matchingAttemptID: expectedAttemptID
                    ))
                }
            }
        } catch {
            completion(nil)
        }
    }

    private func applyProbeFailure(
        baseMessage: String,
        diagnostics: String?,
        connection: NEVPNConnection,
        shouldStop: Bool
    ) {
        activeStartAttemptID = nil
        startWatchdogTask?.cancel()
        startWatchdogTask = nil
        engine?.lastError = diagnostics.map { "\(baseMessage)\n诊断：\($0)" } ?? baseMessage
        guard shouldStop else { return }
        dataPathProbeID = nil
        let tunnelIsActive = connection.status == .connected || connection.status == .reasserting
        if ProbeFailureReconnectPolicy.shouldSuppressAutomaticReconnect(
            shouldStop: shouldStop,
            tunnelIsActive: tunnelIsActive
        ) {
            // Only consume the next disconnected event when this exact failure
            // actually initiates a stop. A diagnostics-only first pass must not
            // leave a suppression flag behind if a newer probe supersedes it.
            engine?.suppressNextAutomaticReconnect()
            engine?.applySystemStatus("disconnecting")
            stopAfterSuspendingOnDemand {
                connection.stopVPNTunnel()
            }
        }
    }

    private func reportEarlyStartFailureIfNeeded() {
        guard let attemptID = activeStartAttemptID else { return }
        activeStartAttemptID = nil
        startWatchdogTask?.cancel()
        startWatchdogTask = nil
        let snapshot = TunnelDiagnosticFormatter.persistedSnapshot(matchingAttemptID: attemptID)
        if !OnDemandStartEnvelopeFailurePolicy.isTransient(stage: snapshot?["stage"] as? String) {
            engine?.suppressNextAutomaticReconnect()
        }
        let diagnostics = snapshot.flatMap { TunnelDiagnosticFormatter.summary($0) }
        let message = "连接启动失败"
        engine?.lastError = diagnostics.map { "\(message)\n诊断：\($0)" } ?? message
        appLog("TunnelManager.start: asynchronous failure diagnostics=\(diagnostics ?? "unavailable")")
    }

    // MARK: - Start / Stop (TunnelManaging)
    //
    // 旧版本曾把启动参数放进 App Group。新版本全部走一次性 start options，
    // 这里只保留同名 key 做迁移清理，避免设备升级后残留敏感配置。
    private enum LegacyAppGroupKey {
        static let config = "xdial.tunnel.config"
        static let server = "xdial.tunnel.server"
        static let username = "xdial.tunnel.username"
        static let password = "xdial.tunnel.password"
    }

    private func clearLegacyStartOptions() {
        guard let defaults = UserDefaults(suiteName: kAppGroupIdentifier) else { return }
        for key in [LegacyAppGroupKey.server, LegacyAppGroupKey.username,
                    LegacyAppGroupKey.password, LegacyAppGroupKey.config] {
            defaults.removeObject(forKey: key)
        }
    }

    private func beginStartOperation(
        completion: @escaping TunnelStartOperationState.Completion
    ) -> UUID {
        let registration = startOperationState.begin(completion: completion)
        registration.cancelled?(.failure(CancellationError()))
        return registration.id
    }

    @discardableResult
    private func finishStartOperation(
        _ operationID: UUID,
        result: Result<Void, Error>
    ) -> Bool {
        guard let completion = startOperationState.finish(operationID) else { return false }
        completion(result)
        return true
    }

    /// 把 profile 转成 NE 模式 sing-box 配置，确保系统隧道配置已保存，
    /// 再通过一次性 start options 把凭据和完整配置交给扩展，不落进 App Group。
    ///
    // TODO: 无法离线验证,真机联调时需要重点检查这里的实际行为。
    // startVPNTunnel() 需要 saveToPreferences 已成功且 NE entitlement 就绪,
    // 否则会抛 NEVPNError;这里只做结构正确性保证,运行时行为等真机验证。
    func startTunnel(profile: Profile, anyConnect: AnyConnectCredentials?,
                      allowSystemOnDemand: Bool,
                      origin: ConnectionTimeline.StartOrigin,
                      completion: @escaping @Sendable (Result<Void, Error>) -> Void) {
        let operationID = beginStartOperation(completion: completion)
        activeStartAllowsSystemOnDemand = allowSystemOnDemand
        let sessionID = UUID().uuidString
        timelineSessionID = sessionID
        timeline?.beginSession(sessionID, markActive: false, origin: origin)
        recordTimeline("start_requested", detail: "anyconnect=\(anyConnect != nil) ondemand=\(allowSystemOnDemand)")
        guard isCurrentStartOperation(operationID) else { return }
        guard UserDefaults(suiteName: kAppGroupIdentifier) != nil else {
            finishStartOperation(operationID, result: .failure(NSError(domain: "XDial", code: -2,
                userInfo: [NSLocalizedDescriptionKey: "App Group UserDefaults 不可用(\(kAppGroupIdentifier))"])))
            return
        }
        let basePath = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: kAppGroupIdentifier)?.path ?? ""

        let profileJSON: String
        let allowInsecure: Bool
        let acceptancePlan: TunnelAcceptancePlan
        do {
            if let anyConnect {
                allowInsecure = try selectedAnyConnectLine(
                    in: profile,
                    credentials: anyConnect
                ).allowInsecure
            } else {
                allowInsecure = false
            }
            let data = try JSONEncoder().encode(profile)
            guard let json = String(data: data, encoding: .utf8) else {
                throw NSError(domain: "XDial", code: -3, userInfo: [NSLocalizedDescriptionKey: "profile JSON 编码失败(非 UTF8)"])
            }
            profileJSON = json
            acceptancePlan = try TunnelAcceptancePlan.make(
                profile: profile,
                profileJSON: json,
                anyConnect: anyConnect
            )
        } catch {
            finishStartOperation(operationID, result: .failure(error))
            return
        }
        activeAcceptancePlan = acceptancePlan
        let server = anyConnect?.server ?? ""

        // 国内下不到 GitHub 上的规则：先把随 App 带的那份放进缓存，保证没网也有一份能用。
        // 只是拷两个几十 KB 的本地文件，不涉及网络。
        let seeded = BundledRuleSets.seedCache(for: profile, basePath: basePath)
        if !seeded.isEmpty {
            appLog("startTunnel: seeded bundled rule sets \(seeded.joined(separator: ","))")
        }

        // 远程规则会在这里通过严格链路预取并落为 App Group 本地文件；绝不能在
        // MainActor 同步等待网络。准备期间立即进入 connecting，完成后再保存系统配置。
        engine?.lastError = nil
        engine?.applySystemStatus("connecting")
        let owner = UncheckedBox(value: self)
        let scenarioID = profile.activeScenarioID
        let scenarioName = profile.scenarios.first { $0.id == scenarioID }?.name ?? ""
        let generationStartedAt = ProcessInfo.processInfo.systemUptime
        Self.configGenerationQueue.submit {
            let outcome = Self.generateStartSession(
                profileJSON: profileJSON,
                server: server,
                basePath: basePath,
                scenarioID: scenarioID,
                scenarioName: scenarioName
            )
            let outcomeBox = UncheckedBox(value: outcome)
            Task { @MainActor in
                let manager = owner.value
                guard manager.isCurrentStartOperation(operationID) else { return }
                let generationMS = ConnectionTimeline.elapsedMilliseconds(since: generationStartedAt)
                switch outcomeBox.value {
                case .failure(let error):
                    manager.recordTimeline("config_generated", result: .failed, ms: generationMS,
                                           detail: error.localizedDescription)
                    manager.engine?.applySystemStatus("disconnected")
                    manager.finishStartOperation(operationID, result: .failure(error))
                case .success(let session):
                    manager.recordTimeline("config_generated", result: .ok, ms: generationMS)
                    manager.startPreparedTunnel(
                        operationID: operationID,
                        configJSON: session.configJSON,
                        switchParameters: session.parameters,
                        allowInsecure: allowInsecure,
                        anyConnect: anyConnect,
                        allowSystemOnDemand: allowSystemOnDemand
                    )
                }
            }
        }
    }

    /// 一次启动所需的 sing-box 配置，以及启动包里额外带上的场景切换参数（iOS）。
    struct StartSession: Sendable {
        let configJSON: String
        let parameters: [String: String]
    }

    /// 生成启动配置。iOS 另外取回线路运行身份，连接中的场景切换据此借用已连上的线路。
    nonisolated static func generateStartSession(
        profileJSON: String,
        server: String,
        basePath: String,
        scenarioID: String,
        scenarioName: String,
        cachedOnly: Bool = false
    ) -> Result<StartSession, NSError> {
        guard profileJSON.utf8.count <= kMaxNEConfigBytes else {
            return .failure(NSError(domain: "XDial", code: -5,
                userInfo: [NSLocalizedDescriptionKey: "连接配置过大，无法安全启动"]))
        }
        var genError: NSError?
        #if os(iOS)
        let raw: String
        if cachedOnly {
            raw = LibboxGenerateCachedNESession(profileJSON, server, basePath, &genError)
        } else {
            let result = LibboxGenerateNESessionResult(profileJSON, server, basePath)!
            if !result.message.isEmpty {
                if result.retryable {
                    return .success(StartSession(configJSON: "{}", parameters: [
                        "preparation_profile": profileJSON,
                        ScenarioStartParameter.scenarioID: scenarioID,
                        ScenarioStartParameter.scenarioName: scenarioName,
                    ]))
                }
                return .failure(NSError(domain: "XDial", code: -5,
                    userInfo: [NSLocalizedDescriptionKey: result.message]))
            }
            raw = result.value
        }
        if let genError { return .failure(genError) }
        guard let data = raw.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let configJSON = object["config_json"] as? String else {
            return .failure(NSError(domain: "XDial", code: -5,
                userInfo: [NSLocalizedDescriptionKey: "连接配置无效"]))
        }
        var parameters = [
            ScenarioStartParameter.scenarioID: scenarioID,
            ScenarioStartParameter.scenarioName: scenarioName,
        ]
        if let line = object["anyconnect"] as? [String: Any],
           let lineID = line["line_id"] as? String,
           let identity = line["runtime_identity"] as? String {
            parameters[ScenarioStartParameter.anyConnectLineID] = lineID
            parameters[ScenarioStartParameter.anyConnectRuntimeIdentity] = identity
        }
        // Tailscale 仍放在各代 Box 里：iOS 配置会带上所有启用的 Tailscale 线路，而由进程持有的
        // Tailscale 只支持一条。两边都用到 Tailscale 的切换由扩展换引擎完成。
        #else
        let result = LibboxGenerateNEConfigResult(profileJSON, server, basePath)!
        if !result.message.isEmpty {
            if result.retryable {
                return .success(StartSession(configJSON: "{}", parameters: ["preparation_profile": profileJSON]))
            }
            return .failure(NSError(domain: "XDial", code: -5,
                userInfo: [NSLocalizedDescriptionKey: result.message]))
        }
        let configJSON = result.value
        let parameters: [String: String] = [:]
        #endif
        guard configJSON.utf8.count <= kMaxNEConfigBytes else {
            return .failure(NSError(domain: "XDial", code: -5,
                userInfo: [NSLocalizedDescriptionKey: "连接配置过大，无法安全启动"]))
        }
        return .success(StartSession(configJSON: configJSON, parameters: parameters))
    }

    private func startPreparedTunnel(
        operationID: UUID,
        configJSON: String,
        switchParameters: [String: String],
        allowInsecure: Bool,
        anyConnect: AnyConnectCredentials?,
        allowSystemOnDemand: Bool
    ) {
        if !allowSystemOnDemand {
            suspendSystemOnDemand(clearEnvelope: true) { [weak self] in
                self?.startPreparedTunnelAfterOnDemandSuspended(
                    operationID: operationID,
                    configJSON: configJSON,
                    switchParameters: switchParameters,
                    allowInsecure: allowInsecure,
                    anyConnect: anyConnect,
                    allowSystemOnDemand: false
                )
            }
            return
        }
        startPreparedTunnelAfterOnDemandSuspended(
            operationID: operationID,
            configJSON: configJSON,
            switchParameters: switchParameters,
            allowInsecure: allowInsecure,
            anyConnect: anyConnect,
            allowSystemOnDemand: true
        )
    }

    private func startPreparedTunnelAfterOnDemandSuspended(
        operationID: UUID,
        configJSON: String,
        switchParameters: [String: String],
        allowInsecure: Bool,
        anyConnect: AnyConnectCredentials?,
        allowSystemOnDemand: Bool
    ) {
        guard isCurrentStartOperation(operationID) else { return }
        guard UserDefaults(suiteName: kAppGroupIdentifier) != nil else {
            engine?.applySystemStatus("disconnected")
            finishStartOperation(operationID, result: .failure(NSError(domain: "XDial", code: -2,
                userInfo: [NSLocalizedDescriptionKey: "App Group UserDefaults 不可用(\(kAppGroupIdentifier))"])))
            return
        }

        clearLegacyStartOptions()
        _ = OnDemandStartEnvelopeStore.clear()
        if allowSystemOnDemand,
           let activeAcceptancePlan,
           let planData = try? JSONEncoder().encode(activeAcceptancePlan),
           let planJSON = String(data: planData, encoding: .utf8) {
            var parameters = [
                "acceptance_plan": planJSON,
                "allow_insecure": allowInsecure ? "true" : "false",
            ]
            if let anyConnect {
                parameters["server"] = anyConnect.server
                parameters["username"] = anyConnect.username
                parameters["password"] = anyConnect.password
            }
            parameters.merge(switchParameters) { current, _ in current }
            pendingOnDemandEnvelope = OnDemandStartEnvelope(
                transport: anyConnect == nil ? "standalone" : "anyconnect",
                parameters: parameters,
                configJSON: configJSON
            )
        } else {
            pendingOnDemandEnvelope = nil
        }

        ensureConfigured(
            serverAddress: anyConnect?.server ?? "XDial",
            operationID: operationID
        ) { [weak self] result in
            // ensureConfigured 的 completion 类型是 @Sendable,闭包字面量默认不再
            // 继承外层 TunnelManager(@MainActor)的隔离——显式 Task { @MainActor in }
            // 重新进入主 actor 才能碰 self.manager(MainActor 隔离属性)。
            Task { @MainActor in
                guard let self else { return }
                guard self.isCurrentStartOperation(operationID) else { return }
                switch result {
                case .failure(let error):
                    self.engine?.applySystemStatus("disconnected")
                    self.finishStartOperation(operationID, result: .failure(error))
                case .success:
                    var savedForNetworkRecovery = false
                    do {
                        guard self.isCurrentStartOperation(operationID) else { return }
                        guard let session = self.manager?.connection as? NETunnelProviderSession else {
                            throw NSError(domain: "XDial", code: -4,
                                userInfo: [NSLocalizedDescriptionKey: "系统隧道会话未加载"])
                        }
                        var options: [String: NSObject] = [
                            "configJSON": configJSON as NSString,
                            "usesAnyConnect": NSNumber(value: anyConnect != nil),
                            "allowInsecure": NSNumber(value: allowInsecure),
                        ]
                        if let anyConnect {
                            options["server"] = anyConnect.server as NSString
                            options["username"] = anyConnect.username as NSString
                            options["password"] = anyConnect.password as NSString
                        }
                        for (key, value) in switchParameters {
                            options[key] = value as NSString
                        }
                        if !switchParameters.isEmpty,
                           let activeAcceptancePlan = self.activeAcceptancePlan,
                           let planData = try? JSONEncoder().encode(activeAcceptancePlan),
                           let planJSON = String(data: planData, encoding: .utf8) {
                            options["acceptance_plan"] = planJSON as NSString
                        }
                        let attemptID = self.timelineSessionID ?? UUID().uuidString
                        self.recordTimeline("tunnel_start_sent")
                        self.dataPathProbeTask?.cancel()
                        self.dataPathProbeTask = nil
                        self.dataPathProbeID = nil
                        self.startWatchdogTask?.cancel()
                        self.activeStartAttemptID = attemptID
                        TunnelDiagnosticFormatter.recordLaunchRequested(attemptID: attemptID)
                        var startOptions = options
                        startOptions["attemptID"] = attemptID as NSString
                        self.engine?.lastError = nil
                        self.engine?.applySystemStatus("connecting")
                        self.startWatchdogTask = Task { @MainActor [weak self, weak session] in
                            try? await Task.sleep(nanoseconds: 45_000_000_000)
                            guard !Task.isCancelled, let self,
                                  self.activeStartAttemptID == attemptID else { return }
                            self.activeStartAttemptID = nil
                            self.dataPathProbeTask?.cancel()
                            self.dataPathProbeTask = nil
                            self.dataPathProbeID = nil
                            let diagnostics = TunnelDiagnosticFormatter.persistedSummary(
                                matchingAttemptID: attemptID
                            )
                            self.recordTimeline("start_watchdog_expired", result: .timeout, ms: 45_000)
                            let message = "连接启动超时"
                            self.engine?.lastError = diagnostics.map {
                                "\(message)\n诊断：\($0)"
                            } ?? message
                            self.engine?.suppressNextAutomaticReconnect()
                            self.engine?.applySystemStatus("disconnecting")
                            // Bound this start without cancelling the user's recovery
                            // request. A terminal provider error revokes the envelope.
                            if (self.systemOnDemandActive || self.onDemandSaveID != nil),
                               case .available = OnDemandStartEnvelopeStore.load() {
                                session?.stopVPNTunnel()
                                self.startWatchdogTask = nil
                                return
                            }
                            self.stopAfterSuspendingOnDemand {
                                session?.stopVPNTunnel()
                            }
                            self.startWatchdogTask = nil
                        }
                        // Persist before submitting the manual start so an immediate
                        // failure can revoke it. Arm only after submission so iOS
                        // cannot race the user's first start with a cold start.
                        if allowSystemOnDemand, self.systemOnDemandRequested, !self.systemOnDemandSuspendedForSetup,
                           let envelope = self.pendingOnDemandEnvelope {
                            savedForNetworkRecovery = OnDemandStartEnvelopeStore.save(envelope) == errSecSuccess
                            startOptions["onDemandEnvelopeSaved"] = NSNumber(value: savedForNetworkRecovery)
                        }
                        try session.startTunnel(options: startOptions)
                        if savedForNetworkRecovery, let manager = self.manager {
                            self.enableSystemOnDemand(manager)
                        }
                        self.finishStartOperation(operationID, result: .success(()))
                    } catch {
                        guard self.isCurrentStartOperation(operationID) else { return }
                        let cause = error as NSError
                        if savedForNetworkRecovery, let manager = self.manager,
                           cause.domain == NEVPNConnectionErrorDomain,
                           cause.code == NEVPNConnectionError.noNetworkAvailable.rawValue {
                            self.engine?.suppressNextAutomaticReconnect()
                            self.enableSystemOnDemand(manager)
                        } else {
                            _ = OnDemandStartEnvelopeStore.clear()
                        }
                        self.activeStartAttemptID = nil
                        self.startWatchdogTask?.cancel()
                        self.startWatchdogTask = nil
                        self.engine?.applySystemStatus("disconnected")
                        self.finishStartOperation(operationID, result: .failure(error))
                    }
                }
            }
        }
    }

    private func selectedAnyConnectLine(
        in profile: Profile,
        credentials: AnyConnectCredentials
    ) throws -> Line {
        guard let scenario = profile.scenarios.first(where: { $0.id == profile.activeScenarioID }) else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "当前场景不可用"])
        }

        let referencedLineIDs = profile.referencedLineIDs(in: scenario)
        let lines = profile.lines.filter {
            referencedLineIDs.contains($0.id) && $0.enabled && $0.type == "vpn"
        }
        guard lines.count == 1,
              let line = lines.first,
              line.vpnServer == credentials.server,
              line.vpnUsername == credentials.username,
              line.vpnPassword == credentials.password else {
            throw NSError(domain: "XDial", code: -6,
                userInfo: [NSLocalizedDescriptionKey: "当前场景的 AnyConnect 线路不唯一或凭据不匹配"])
        }
        return line
    }

    #if os(iOS)
    // MARK: - 场景目录与隧道内切换（iOS）

    private var scenarioCatalogBuildState = ScenarioCatalogBuildState()

    func rebuildScenarioCatalog(
        inputs: [ScenarioCatalogInput],
        policy: ScenarioAutoPolicy,
        keepingOthers: Bool
    ) async -> String? {
        let basePath = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: kAppGroupIdentifier)?.path ?? ""
        guard !basePath.isEmpty, !inputs.isEmpty else { return nil }
        let buildID = scenarioCatalogBuildState.begin(policy: policy)
        let entries: [String: Data] = await withCheckedContinuation { continuation in
            Self.configGenerationQueue.submit {
                var entries: [String: Data] = [:]
                for input in inputs.prefix(ScenarioSwitchCatalog.maxEntries) {
                    guard case .success(let session) = Self.generateStartSession(
                        profileJSON: input.profileJSON,
                        server: input.anyConnect?.server ?? "",
                        basePath: basePath,
                        scenarioID: input.scenarioID,
                        scenarioName: input.scenarioName,
                        cachedOnly: true
                    ) else {
                        appLog("scenario catalog: skipped \(input.scenarioID), config generation failed")
                        continue
                    }
                    var parameters = session.parameters
                    parameters["acceptance_plan"] = input.acceptancePlanJSON
                    parameters["allow_insecure"] = input.allowInsecure ? "true" : "false"
                    if let anyConnect = input.anyConnect {
                        parameters["server"] = anyConnect.server
                        parameters["username"] = anyConnect.username
                        parameters["password"] = anyConnect.password
                    }
                    let envelope = OnDemandStartEnvelope(
                        transport: input.anyConnect == nil ? "standalone" : "anyconnect",
                        parameters: parameters,
                        configJSON: session.configJSON
                    )
                    guard envelope.isValid, let data = try? JSONEncoder().encode(envelope) else { continue }
                    entries[input.scenarioID] = data
                }
                continuation.resume(returning: entries)
            }
        }
        guard let policy = scenarioCatalogBuildState.policyForCompletion(buildID, cancelled: Task.isCancelled),
              !entries.isEmpty else { return nil }
        var merged = entries
        if keepingOthers, case .available(let existing) = ScenarioSwitchCatalogStore.load() {
            for (scenarioID, entry) in existing.entries
            where merged[scenarioID] == nil
                && policy.scenarioIDs.contains(scenarioID)
                && merged.count < ScenarioSwitchCatalog.maxEntries {
                merged[scenarioID] = entry
            }
        }
        let catalog = ScenarioSwitchCatalog(policy: policy, entries: merged)
        let status = ScenarioSwitchCatalogStore.save(catalog)
        guard status == errSecSuccess else {
            appLog("scenario catalog: save failed status=\(status)")
            return nil
        }
        appLog("scenario catalog: saved \(entries.count) scenario(s)")
        return catalog.revision
    }

    func updateScenarioCatalogPolicy(_ policy: ScenarioAutoPolicy) -> Bool {
        scenarioCatalogBuildState.update(policy: policy)
        guard case .available(var catalog) = ScenarioSwitchCatalogStore.load() else { return false }
        guard catalog.policy != policy else { return true }
        catalog.policy = policy
        return ScenarioSwitchCatalogStore.save(catalog) == errSecSuccess
    }

    func requestScenarioSwitch(to scenarioID: String, revision: String, reapply: Bool) async -> ScenarioSwitchReply {
        guard let session = providerSession,
              let request = try? JSONSerialization.data(withJSONObject: [
                  "cmd": reapply ? "scenario-reapply" : "scenario-switch",
                  "scenario_id": scenarioID,
                  "catalog_revision": revision,
                  ScenarioSwitchDeadline.requestKey: ScenarioSwitchDeadline().uptime,
              ]) else {
            return .failed("VPN 扩展不可用", nil)
        }
        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<ScenarioSwitchReply>(continuation)
            // 请求携带整次切换的截止时间；watchdog 额外留出中止、恢复与回包时间。
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + ScenarioSwitchDeadline.replySeconds) {
                oneShot.finish(.failed("VPN 扩展没有及时回应", nil))
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    guard let responseData,
                          let response = try? JSONDecoder().decode(DaemonResponse.self, from: responseData) else {
                        oneShot.finish(.failed("VPN 扩展回应无效", nil))
                        return
                    }
                    let state = response.data
                        .flatMap { $0.data(using: .utf8) }
                        .flatMap(TunnelScenarioState.decode)
                    if response.ok == true, let state {
                        oneShot.finish(.switched(state))
                    } else if response.message?.hasPrefix("未知命令") == true {
                        oneShot.finish(.unsupported)
                    } else {
                        oneShot.finish(.failed(response.message ?? "切换场景失败", state))
                    }
                }
            } catch {
                oneShot.finish(.failed(error.localizedDescription, nil))
            }
        }
    }

    func requestScenarioEvaluation() async -> ScenarioEvaluationReply {
        guard let session = providerSession else { return .unavailable }
        guard let request = try? JSONSerialization.data(withJSONObject: [
            "cmd": "scenario-evaluate",
            ScenarioSwitchDeadline.requestKey: ScenarioSwitchDeadline().uptime,
        ]) else { return .unavailable }
        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<ScenarioEvaluationReply>(continuation)
            // 读 Wi-Fi 名称最多 3 秒、查出口最多 8 秒，判断出要切换时还要等切换结束。
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + ScenarioSwitchDeadline.replySeconds) {
                oneShot.finish(.unavailable)
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    guard let responseData,
                          let response = try? JSONDecoder().decode(DaemonResponse.self, from: responseData) else {
                        oneShot.finish(.unavailable)
                        return
                    }
                    if response.ok != true, response.message?.hasPrefix("未知命令") == true {
                        oneShot.finish(.unsupported)
                        return
                    }
                    let state = response.data
                        .flatMap { $0.data(using: .utf8) }
                        .flatMap(TunnelScenarioState.decode)
                    oneShot.finish(state.map(ScenarioEvaluationReply.evaluated) ?? .unavailable)
                }
            } catch {
                oneShot.finish(.unavailable)
            }
        }
    }

    func requestScenarioState() async -> TunnelScenarioState? {
        guard let session = providerSession else { return nil }
        let request = Data("{\"cmd\":\"scenario-state\"}".utf8)
        return await withCheckedContinuation { continuation in
            let oneShot = OneShotContinuation<TunnelScenarioState?>(continuation)
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 3) {
                oneShot.finish(nil)
            }
            do {
                try session.sendProviderMessage(request) { responseData in
                    let state = responseData
                        .flatMap { try? JSONDecoder().decode(DaemonResponse.self, from: $0) }
                        .flatMap { $0.ok == true ? $0.data : nil }
                        .flatMap { $0.data(using: .utf8) }
                        .flatMap(TunnelScenarioState.decode)
                    oneShot.finish(state)
                }
            } catch {
                oneShot.finish(nil)
            }
        }
    }

    func adoptSwitchedScenario(_ scenarioID: String) {
        guard case .available(let catalog) = ScenarioSwitchCatalogStore.load(),
              let data = catalog.entries[scenarioID],
              let envelope = try? JSONDecoder().decode(OnDemandStartEnvelope.self, from: data),
              let planJSON = envelope.parameters["acceptance_plan"],
              let planData = planJSON.data(using: .utf8),
              let plan = try? JSONDecoder().decode(TunnelAcceptancePlan.self, from: planData) else {
            appLog("scenario switch: acceptance plan for the new scenario is unavailable")
            return
        }
        // 按需连接还没开启时，待开启的启动包也换成新场景，免得开启时写回旧场景。
        if pendingOnDemandEnvelope != nil {
            pendingOnDemandEnvelope = envelope
        }
        guard plan != activeAcceptancePlan else {
            if dataPathVerified { engine?.dataPathDidPass.send() }
            return
        }
        activeAcceptancePlan = plan
        guard let connection = manager?.connection, connection.status == .connected else { return }
        engine?.lineProbeReadings = []
        // 切换不断开连接：自检没通过时保留连接并提示，不按首次连接的规则断开。
        verifyDataPath(for: connection, isRecheck: true)
    }
    #endif

    private func stopAfterSuspendingOnDemand(_ stop: @escaping @MainActor () -> Void) {
        timeline?.recordAppStopRequest(fallbackSession: timelineSessionID ?? "")
        suspendSystemOnDemand(clearEnvelope: true, completion: stop)
    }

    /// 停止系统级隧道。manager/connection 不可用时静默返回(语义等价「本来就没在跑」)。
    func stopTunnel() {
        let cancelledCompletion = startOperationState.cancel()
        activeStartAttemptID = nil
        startWatchdogTask?.cancel()
        startWatchdogTask = nil
        dataPathProbeTask?.cancel()
        dataPathProbeTask = nil
        dataPathProbeID = nil
        engine?.applySystemStatus("disconnecting")
        let connection = manager?.connection
        #if os(iOS)
        // 用户断开后不再有扩展使用场景目录；下次连接会重建。
        scenarioCatalogBuildState.clear()
        ScenarioSwitchCatalogStore.clear()
        #endif
        stopAfterSuspendingOnDemand {
            connection?.stopVPNTunnel()
        }
        cancelledCompletion?(.failure(CancellationError()))
    }

    // MARK: - Convenience

    /// 从活动线路的默认出口 Line 推导一个展示用 serverAddress(纯 UI 用途)。
    /// 找不到就返回空串,由 ensureConfigured 兜底成占位。
    static func serverAddress(for profile: Profile) -> String {
        guard let scenario = profile.scenarios.first(where: { $0.id == profile.activeScenarioID }),
              let line = profile.lines.first(where: { $0.id == scenario.defaultLineID }) else {
            return ""
        }
        switch line.type {
        case "vpn": return line.vpnServer
        case "trojan": return line.trojanServer
        case "shadowsocks", "ss": return line.ssServer
        case "vmess": return line.vmessServer
        default: return ""  // direct 等无远端地址
        }
    }

    /// 系统连接描述只需要一个可识别的主机名。绝不能把 URL userinfo、路径或
    /// query 持久化进系统偏好；真实拨号仍使用一次性 start options 里的原值。
    static func systemProfileServerAddress(_ rawValue: String) -> String {
        let trimmed = rawValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "XDial" }

        func hostAndPort(from components: URLComponents?) -> String? {
            guard let components, let host = components.host, !host.isEmpty else { return nil }
            let renderedHost = host.contains(":") ? "[\(host)]" : host
            return components.port.map { "\(renderedHost):\($0)" } ?? renderedHost
        }

        if let value = hostAndPort(from: URLComponents(string: trimmed)) {
            return value
        }
        if let value = hostAndPort(from: URLComponents(string: "https://" + trimmed)) {
            return value
        }
        return "XDial"
    }
}
