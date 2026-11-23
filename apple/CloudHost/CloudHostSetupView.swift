import SwiftUI
import UniformTypeIdentifiers

/// The "add my server" wizard, shared by iPhone and Mac. The host decides how
/// a verified line is saved (`onSave`) and may block saving while connected.
struct CloudHostSetupView: View {
    @ObservedObject var model: CloudHostSetupModel
    let chinese: Bool
    /// Why saving is not possible right now, if anything.
    var saveBlockedReason: String?
    /// Returns false if the line could not be saved.
    let onSave: (CloudHostLineDraft) -> Bool
    let onClose: () -> Void

    @State private var importingKey = false

    private func t(_ zh: String, _ en: String) -> String { chinese ? zh : en }

    var body: some View {
        Form {
            switch model.stage {
            case .server, .scanning:
                serverForm
            case .confirmKey(let key):
                confirmKey(key)
            case .inspecting:
                progressSection(t("正在只读检查服务器，不会做任何改动…", "Inspecting the server read-only; nothing is changed…"))
            case .review(let report):
                review(report)
            case .deploying:
                deploying
            case .verifying:
                progressSection(t("正在经新代理访问 HTTPS 并测速…", "Testing HTTPS and speed through the new proxy…"))
            case .verified(let deployed, let measurement):
                verified(deployed, measurement)
            case .unverified(let deployed, let message):
                unverified(deployed, message)
            case .saved:
                Section {
                    Label(t("已保存。可以在场景里选择这条线路。", "Saved. You can now choose this line in a scenario."), systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                }
            }
            if let error = model.error {
                Section {
                    Label(error.text(chinese), systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("cloud-host-error")
                }
            }
        }
        .formStyle(.grouped)
        .onAppear { model.lineNamePrefix = t("云主机", "My Server") }
        .navigationTitle(t("添加我的云主机", "Add My Server"))
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button(model.stage == .saved ? t("完成", "Done") : t("关闭", "Close")) {
                    model.close()
                    onClose()
                }
                .disabled(model.stage == .deploying)
            }
        }
        .fileImporter(isPresented: $importingKey, allowedContentTypes: [.data, .text]) { result in
            guard case .success(let url) = result else { return }
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            if let data = try? Data(contentsOf: url, options: [.uncached]), data.count < 64 * 1024 {
                model.privateKey = String(decoding: data, as: UTF8.self)
            }
        }
    }

    // MARK: Steps

    @ViewBuilder
    private var serverForm: some View {
        Section {
            field(t("地址", "Address"), prompt: t("IP 或域名", "IP or domain"), text: $model.host, id: "cloud-host-address", plain: true)
            field(t("SSH 端口", "SSH port"), prompt: "22", text: $model.sshPort, id: "cloud-host-ssh-port", numeric: true)
            field(t("用户名", "User name"), prompt: "root", text: $model.user, id: "cloud-host-user", plain: true)
            Picker(t("登录方式", "Sign in with"), selection: $model.authentication) {
                Text(t("密码", "Password")).tag(CloudHostSetupModel.Authentication.password)
                Text(t("私钥", "Private key")).tag(CloudHostSetupModel.Authentication.privateKey)
            }
            switch model.authentication {
            case .password:
                SecureField(t("密码", "Password"), text: $model.password)
                    .textContentType(.password)
            case .privateKey:
                TextEditor(text: $model.privateKey)
                    .font(.system(.caption, design: .monospaced))
                    .frame(minHeight: 90)
                    .autocorrectionDisabled()
                    #if os(iOS)
                    .textInputAutocapitalization(.never)
                    #endif
                    .overlay(alignment: .topLeading) {
                        if model.privateKey.isEmpty {
                            Text(t("粘贴私钥（-----BEGIN … PRIVATE KEY-----）", "Paste the private key (-----BEGIN … PRIVATE KEY-----)"))
                                .font(.caption).foregroundStyle(.secondary).padding(6).allowsHitTesting(false)
                        }
                    }
                Button(t("从文件读取私钥…", "Read Key from File…")) { importingKey = true }
                SecureField(t("私钥密码短语（如有）", "Key passphrase (if any)"), text: $model.passphrase)
                SecureField(t("sudo 密码（非 root 且需要时）", "sudo password (if needed)"), text: $model.password)
            }
        } header: {
            Text(t("你的服务器", "Your server"))
        } footer: {
            Text(t("先支持 Ubuntu 20.04+ 和 Debian 12+（amd64、arm64），需要 root 或 sudo 权限。密码和私钥只在这台设备的内存里用于这次设置，不会保存，也不会写进日志。",
                   "Ubuntu 20.04+ and Debian 12+ (amd64, arm64) with root or sudo access are supported. The password and private key are used only in this device's memory for this setup; they are never saved or logged."))
        }
        Section {
            Button {
                Task { await model.scanHostKey() }
            } label: {
                if model.stage == .scanning {
                    ProgressView()
                } else {
                    Text(t("下一步：核对服务器指纹", "Next: Check the Server Fingerprint"))
                }
            }
            .disabled(!model.canScan)
            .accessibilityIdentifier("cloud-host-scan")
        }
    }

    @ViewBuilder
    private func confirmKey(_ key: CloudHostKey) -> some View {
        Section {
            Text(key.fingerprint)
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
                .accessibilityIdentifier("cloud-host-fingerprint")
            LabeledContent(t("密钥类型", "Key type"), value: key.algorithm)
        } header: {
            Text(t("核对服务器指纹", "Check the server fingerprint"))
        } footer: {
            Text(t("还没有发送密码或私钥。请在云服务商控制台或服务器上核对这串指纹，例如在服务器上运行 ssh-keygen -lf \(key.file ?? "/etc/ssh/ssh_host_ed25519_key.pub")。指纹不一致时不要继续。",
                   "No password or key has been sent yet. Compare this fingerprint with your provider's console or the server itself, for example by running ssh-keygen -lf \(key.file ?? "/etc/ssh/ssh_host_ed25519_key.pub") on the server. Do not continue if it differs."))
        }
        Section {
            Button(t("指纹一致，登录并检查", "It Matches — Sign In and Inspect")) {
                Task { await model.confirmHostKey(key) }
            }
            .disabled(model.working)
            .accessibilityIdentifier("cloud-host-confirm-key")
            Button(t("返回修改", "Back"), role: .cancel) { model.back() }
        }
    }

    @ViewBuilder
    private func review(_ report: CloudHostReport) -> some View {
        Section(t("服务器", "Server")) {
            LabeledContent(t("系统", "System"), value: report.system)
            LabeledContent(t("架构", "Architecture"), value: report.architecture)
            LabeledContent(t("权限", "Access"), value: privilegeText(report.privilege))
            if let version = report.singBoxVersion, !version.isEmpty {
                LabeledContent("sing-box", value: version)
            }
        }
        if !report.blocking.isEmpty {
            Section {
                ForEach(report.blocking, id: \.code) { item in
                    Label(item.text(chinese), systemImage: "xmark.octagon.fill").foregroundStyle(.red)
                }
                Button(t("返回", "Back"), role: .cancel) { model.back() }
            } header: {
                Text(t("无法在这台服务器上设置", "This server cannot be set up"))
            }
        } else {
            if let existing = report.existing {
                Section {
                    Label(t("这台服务器已有 XDial 代理（端口 \(existing.port)）。保持相同协议与端口会直接使用它。",
                            "This server already has an XDial proxy (port \(existing.port)). Keeping the same protocol and port reuses it."),
                          systemImage: "arrow.triangle.2.circlepath")
                }
            }
            Section {
                Picker(t("协议", "Protocol"), selection: $model.protocolKind) {
                    Text(t("VLESS + Reality（推荐）", "VLESS + Reality (Recommended)")).tag(CloudHostSetupModel.ProtocolKind.vlessReality)
                    Text("Shadowsocks 2022").tag(CloudHostSetupModel.ProtocolKind.shadowsocks)
                }
                .onChange(of: model.protocolKind) { _, _ in
                    Task { await model.refreshPlan() }
                }
                field(t("代理端口", "Proxy port"),
                      prompt: model.protocolKind == .vlessReality ? "443 / 1024–65535" : "1024–65535",
                      text: $model.proxyPort, id: "cloud-host-port", numeric: true)
                    .onSubmit { Task { await model.refreshPlan() } }
                if model.protocolKind == .vlessReality {
                    field(t("伪装域名（SNI）", "Camouflage SNI"),
                          prompt: "www.microsoft.com",
                          text: $model.realityServerName, id: "cloud-host-sni", plain: true)
                        .onSubmit { Task { await model.refreshPlan() } }
                }
                Button(t("按当前选项重新生成改动清单", "Update the Plan")) { Task { await model.refreshPlan() } }
                if model.planning {
                    ProgressView(t("正在生成改动清单…", "Updating the plan…"))
                } else if let planError = model.planError {
                    Text(planError.text(chinese)).foregroundStyle(.red)
                } else if model.plan == nil, !model.proxyPort.isEmpty {
                    Text(t("配置已更改，请重新生成改动清单。", "Options changed; update the plan."))
                        .foregroundStyle(.secondary)
                }
            } header: {
                Text(model.protocolKind == .vlessReality
                     ? t("代理协议与端口（VLESS + Reality，无需自有域名或证书）", "Protocol and port (VLESS + Reality, no domain or certificate needed)")
                     : t("代理协议与端口（Shadowsocks 2022，TCP 和 UDP）", "Protocol and port (Shadowsocks 2022, TCP and UDP)"))
            }
            if let plan = model.plan {
                Section {
                    if plan.steps.isEmpty {
                        Text(t("不需要改动，直接使用已有的代理。", "Nothing to change; the existing proxy is used."))
                    }
                    ForEach(Array(plan.steps.enumerated()), id: \.offset) { index, step in
                        VStack(alignment: .leading, spacing: 2) {
                            Text("\(index + 1). " + step.text(chinese))
                            if let detail = step.detail, !detail.isEmpty {
                                Text(detail).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                } header: {
                    Text(t("将在服务器上做这些改动", "These changes will be made on the server"))
                } footer: {
                    Text(t("不会修改 SSH、默认路由或已有服务，也不会关闭防火墙。任何一步失败或你取消时，会撤销本次所做的改动。",
                           "SSH, the default route and existing services are not changed, and the firewall is not turned off. If a step fails or you cancel, this run's changes are undone."))
                }
                Section {
                    Text(CloudHostDisclosure.test(chinese)).fixedSize(horizontal: false, vertical: true)
                    Text(CloudHostDisclosure.visibility(chinese)).font(.callout).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } header: {
                    Text(t("部署后会这样测试", "How the proxy is tested after setup"))
                }
                Section {
                    Button(plan.reuse ? t("使用已有代理", "Use the Existing Proxy") : t("确认并部署", "Confirm and Set Up")) {
                        Task { await model.deploy() }
                    }
                    .disabled(!model.canDeploy)
                    .accessibilityIdentifier("cloud-host-deploy")
                }
            }
            if !report.notes.isEmpty {
                Section(t("注意", "Notes")) {
                    ForEach(report.notes, id: \.code) { item in
                        Text(item.text(chinese)).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var deploying: some View {
        Section {
            ForEach(model.stepProgress, id: \.step) { event in
                HStack(alignment: .firstTextBaseline) {
                    Image(systemName: eventSymbol(event.status)).foregroundStyle(eventColor(event.status))
                    VStack(alignment: .leading) {
                        Text(stepTitle(event.step) + " · " + statusText(event.status))
                        if let message = event.message, !message.isEmpty {
                            Text(message).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            ProgressView()
        } header: {
            Text(t("正在部署", "Setting up"))
        } footer: {
            Text(t("请保持 App 打开和网络连接。如果中途断开，服务器上可能留下这次的部分改动；下次连接这台服务器设置时，会先撤销它们再继续。",
                   "Keep the app open and connected. If the connection drops, some of this run's changes may stay on the server; the next setup on this server undoes them first."))
        }
        Section {
            Button(t("取消并撤销", "Cancel and Undo"), role: .destructive) { model.cancel() }
                .accessibilityIdentifier("cloud-host-cancel")
        }
    }

    @ViewBuilder
    private func verified(_ deployed: CloudHostResult, _ measurement: CloudHostMeasurement) -> some View {
        Section {
            LabeledContent(t("HTTPS 延迟", "HTTPS latency"), value: "\(measurement.latencyMS) ms")
            LabeledContent(t("下载速度", "Download speed"), value: measurement.mbps > 0 ? String(format: "%.1f Mbps", measurement.mbps) : t("未测得", "Not measured"))
            if let ip = measurement.exitIP, !ip.isEmpty {
                LabeledContent(t("出口 IP", "Exit IP"), value: ip)
            }
            LabeledContent(t("地址", "Address"), value: "\(deployed.server):\(deployed.port)")
        } header: {
            Text(t("已经由新代理访问 HTTPS 成功", "HTTPS works through the new proxy"))
        } footer: {
            Text(t("这是这次实际测得的结果。网络状况变化后可能不同，XDial 不能保证长期稳定可用。",
                   "These are this test's actual measurements. Results change with network conditions; XDial cannot promise lasting reliability."))
        }
        Section {
            field(t("线路名称", "Line name"), prompt: model.defaultLineName, text: $model.lineName, id: "cloud-host-line-name")
            Button(t("保存线路", "Save Line")) {
                if let draft = model.draft(), onSave(draft) { model.markSaved() }
            }
            .disabled(saveBlockedReason != nil)
            .accessibilityIdentifier("cloud-host-save")
            if let reason = saveBlockedReason {
                Text(reason).font(.caption).foregroundStyle(.secondary)
            }
            Button(t("重新测试", "Test Again")) { Task { await model.verify(deployed) } }
        } footer: {
            Text(t("保存后它是一条普通线路，不会自动加入任何场景。「重新测试」会再次经服务器下载最多约 10 MB。",
                   "Once saved it is an ordinary line; it is not added to any scenario automatically. Test Again downloads up to about 10 MB through the server again."))
        }
    }

    @ViewBuilder
    private func unverified(_ deployed: CloudHostResult, _ message: CloudHostMessage) -> some View {
        Section {
            Label(message.text(chinese), systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
                .fixedSize(horizontal: false, vertical: true)
            LabeledContent(t("需要放行的端口", "Port to allow"),
                           value: deployed.isVLESSReality ? "\(deployed.port) (TCP)" : "\(deployed.port) (TCP, UDP)")
        } header: {
            Text(t("服务已在服务器上运行，但还没能经它访问 HTTPS", "The service runs on the server, but HTTPS through it has not worked yet"))
        } footer: {
            Text(t("在服务商控制台放行端口后点「重新测试」（会再次经服务器下载最多约 10 MB）。测试通过之前不会保存这条线路。",
                   "Allow the port in your provider's console, then tap Test Again (it downloads up to about 10 MB through the server again). The line is not saved until a test passes."))
        }
        Section {
            Button(t("重新测试", "Test Again")) { Task { await model.verify(deployed) } }
                .disabled(model.working)
                .accessibilityIdentifier("cloud-host-retest")
        }
    }

    /// A labeled text field: macOS forms label fields themselves, iOS needs a row label.
    @ViewBuilder
    private func field(_ label: String, prompt: String, text: Binding<String>, id: String, numeric: Bool = false, plain: Bool = false) -> some View {
        #if os(iOS)
        LabeledContent(label) {
            TextField(prompt, text: text)
                .multilineTextAlignment(.trailing)
                .keyboardType(numeric ? .numberPad : (plain ? .URL : .default))
                .textInputAutocapitalization(plain || numeric ? .never : .sentences)
                .autocorrectionDisabled(plain || numeric)
                .accessibilityIdentifier(id)
        }
        #else
        TextField(label, text: text, prompt: Text(prompt))
            .autocorrectionDisabled(plain || numeric)
            .accessibilityIdentifier(id)
        #endif
    }

    private func progressSection(_ text: String) -> some View {
        Section {
            HStack(spacing: 12) {
                ProgressView()
                Text(text)
            }
        }
    }

    // MARK: Labels

    private func privilegeText(_ privilege: String) -> String {
        switch privilege {
        case "root": return "root"
        case "sudo": return t("sudo（免密码）", "sudo (no password)")
        case "sudo-password": return t("sudo（使用登录密码）", "sudo (with the login password)")
        default: return t("没有管理员权限", "No administrator access")
        }
    }

    private func stepTitle(_ step: String) -> String {
        if let title = model.plan?.steps.first(where: { $0.id == step })?.text(chinese) { return title }
        switch step {
        case "prepare": return t("准备配置目录", "Prepare the configuration folder")
        default: return step
        }
    }

    private func statusText(_ status: String) -> String {
        switch status {
        case "running": return t("进行中", "Running")
        case "done": return t("完成", "Done")
        case "failed": return t("失败", "Failed")
        case "rolling_back": return t("正在撤销", "Undoing")
        case "rolled_back": return t("已撤销", "Undone")
        default: return status
        }
    }

    private func eventSymbol(_ status: String) -> String {
        switch status {
        case "done": return "checkmark.circle.fill"
        case "failed": return "xmark.circle.fill"
        case "rolling_back", "rolled_back": return "arrow.uturn.backward.circle"
        default: return "circle.dotted"
        }
    }

    private func eventColor(_ status: String) -> Color {
        switch status {
        case "done": return .green
        case "failed": return .red
        case "rolling_back", "rolled_back": return .orange
        default: return .secondary
        }
    }
}

extension Line {
    /// A verified line from "add my server". Credentials are stored through the
    /// app's existing credential handling.
    init(cloudHost draft: CloudHostLineDraft) {
        let lineID = "cloud-" + String(UUID().uuidString.prefix(8)).lowercased()
        if draft.isVLESSReality {
            self.init(id: lineID, name: draft.name, type: "vless")
            vlessServer = draft.server
            vlessPort = draft.port
            vlessUUID = draft.uuid
            vlessFlow = draft.flow.isEmpty ? "xtls-rprx-vision" : draft.flow
            vlessSNI = draft.serverName
            vlessClientFingerprint = draft.fingerprint.isEmpty ? "chrome" : draft.fingerprint
            vlessRealityPublicKey = draft.publicKey
            vlessRealityShortID = draft.shortID
            udp = true
        } else {
            self.init(id: lineID, name: draft.name, type: "shadowsocks")
            ssServer = draft.server
            ssPort = draft.port
            ssMethod = draft.method
            ssPassword = draft.password
        }
        enabled = true
        verified = true
    }
}
