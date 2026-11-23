import CryptoKit
import Foundation

enum ProfileLibraryMigration {
    /// Libraries saved by the retired Next identity before the shared library
    /// existed already contain user edits and multiple Profiles.
    /// Preserve the whole latest library before considering older single Profiles.
    static func initialLibrary(
        home: URL = FileManager.default.homeDirectoryForCurrentUser,
        readLibrary: ((ConfigurationStorage.LegacyLocation) throws -> ProfileLibrary?)? = nil,
        readProfile: ((ConfigurationStorage.LegacyLocation) throws -> Profile?)? = nil,
        convertProfile: (Profile, String) throws -> Profile
    ) throws -> ProfileLibrary? {
        func modified(_ path: String) -> Date {
            (try? home.appendingPathComponent(path).resourceValues(forKeys: [.contentModificationDateKey]))?
                .contentModificationDate ?? .distantPast
        }
        let libraries = ConfigurationStorage.legacyLocations.filter {
            FileManager.default.fileExists(atPath: home.appendingPathComponent($0.dataPath + "/profiles.enc").path)
        }.sorted {
            modified($0.dataPath + "/profiles.enc") > modified($1.dataPath + "/profiles.enc")
        }
        if let source = libraries.first {
            let library: ProfileLibrary?
            if let readLibrary { library = try readLibrary(source) }
            else {
                library = try ProfileLibraryStore(directory: home.appendingPathComponent(source.dataPath),
                                                  keychainService: source.keychainService).load()
            }
            guard let library else {
                throw ProfileLibraryError.invalid("旧配置库在转换期间发生变化，请稍后重试")
            }
            return library
        }
        let candidates = try ConfigurationStorage.legacyLocations.filter {
            try ExistingXDialProfileReader.profileData(at: $0, home: home) != nil
        }.sorted { modified($0.preferencesPath) > modified($1.preferencesPath) }
        guard let source = candidates.first else { return nil }
        let original: Profile?
        if let readProfile { original = try readProfile(source) }
        else { original = try ExistingXDialProfileReader.read(source, home: home) }
        guard let original else {
            throw ProfileLibraryError.invalid("旧版配置在转换期间发生变化，请稍后重试")
        }
        let id = UUID().uuidString.lowercased()
        return try library(with: ProfileRecord(id: id, name: "默认配置", profile: convertProfile(original, id)))
    }

    static func starterLibrary() throws -> ProfileLibrary {
        try library(with: ProfileRecord(name: "默认配置", profile: .bootstrap()))
    }

    /// An empty library is created from the seed as is: its Scenarios keep their own names.
    static func seedLibrary(
        _ seed: SeedProfile?,
        id: String = UUID().uuidString.lowercased(),
        importDocument: (String, String) throws -> Profile
    ) throws -> ProfileLibrary? {
        guard let seed else { return nil }
        let imported = try importDocument(seed.text, id)
        return try library(with: ProfileRecord(id: id, name: seed.name, profile: imported, baseline: imported))
    }

    /// Adds the seed beside existing Profiles like a file import, without changing
    /// the active Scenario or the edited Profile. A Wi-Fi trigger already owned by
    /// an existing Scenario stays there instead of rejecting the whole seed.
    static func merging(_ seed: Profile, named name: String, id: String,
                        into library: ProfileLibrary) throws -> ProfileLibrary {
        var imported = seed
        let claimed = Set(library.scenarios.flatMap(\.matchSSIDs))
        for index in imported.scenarios.indices {
            imported.scenarios[index].matchSSIDs.removeAll(where: claimed.contains)
        }
        var result = library
        try result.insert(ProfileRecord(id: id, name: name, profile: imported, baseline: imported))
        result.editingProfileID = library.editingProfileID
        return result
    }

    /// What applying the bundled seed at launch decided. `library` is nil when the
    /// library stays as it is, `entry` is nil when the ledger stays as it is, and
    /// `regionTargets` is nil when the current region targets stay.
    struct SeedApplication {
        var library: ProfileLibrary?
        var entry: SeedProfileLedger.Entry?
        var regionTargets: [String: String]?
        var failure: Error?
    }

    /// The launch-time seed decision, without storage, so tests run exactly what the app runs.
    /// The seed joins the shared library once and later builds update what it owns.
    /// A seed Profile the user deleted is not added again.
    static func applying(_ seed: SeedProfile, createdProfileID: String?, entry: SeedProfileLedger.Entry?,
                         to library: ProfileLibrary, currentRegionTargets: [String: String],
                         importDocument: (String, String) throws -> Profile) -> SeedApplication {
        let profileID = createdProfileID ?? entry?.profileID ?? UUID().uuidString.lowercased()
        if createdProfileID == nil, let entry, entry.isNewer(than: seed) {
            return SeedApplication(regionTargets: entry.regionTargets ?? [:])
        }
        var regions: [String: String]?
        if !seed.regions.isEmpty, let imported = try? importDocument(seed.text, profileID) {
            regions = seed.regionTargets(in: imported)
        }
        // Region targets name imported Scenario IDs; when they collided with IDs already in
        // the library, the Profile records the scoped IDs it uses instead.
        func scoped(_ library: ProfileLibrary) -> [String: String]? {
            guard let regions, let mapping = library.profiles.first(where: { $0.id == profileID })?.resourceIDs else {
                return regions
            }
            return regions.mapValues { mapping[$0] ?? $0 }
        }
        guard entry?.digest != seed.digest else { return SeedApplication(regionTargets: scoped(library)) }
        var updated: ProfileLibrary?
        if createdProfileID == nil {
            if entry != nil && !library.profiles.contains(where: { $0.id == profileID }) {
                return SeedApplication(regionTargets: scoped(library))
            }
            do {
                let incoming = try importDocument(seed.text, profileID)
                if seed.revision != nil {
                    // A ledger digest without the mobile prefix means the old desktop preset created this Profile.
                    let fromLegacySeed = entry.map { !$0.digest.hasPrefix("mobile-v1-") } ?? false
                    updated = try addingMobileSeed(incoming, named: seed.name, id: profileID, to: library,
                                                   aligningLegacySeed: fromLegacySeed,
                                                   ruleSetHistory: seed.ruleSetHistory(in: incoming),
                                                   scenarioHistory: seed.scenarioHistory(in: incoming))
                } else if entry != nil {
                    updated = try refreshingSeed(incoming, profileID: profileID, named: seed.name, in: library)
                } else {
                    updated = try merging(incoming, named: seed.name, id: profileID, into: library)
                }
            } catch {
                return SeedApplication(regionTargets: regions, failure: error)
            }
        }
        let targets = scoped(updated ?? library)
        return SeedApplication(library: updated,
                               entry: .init(profileID: profileID, digest: seed.digest,
                                            regionTargets: targets ?? currentRegionTargets),
                               regionTargets: targets)
    }

    /// Apply one mobile seed revision using the existing global resource owners.
    /// Existing routes, credentials and the selected Scenario are user choices.
    /// `aligningLegacySeed` is set once, when a Profile created by the older
    /// desktop preset (no revision) first receives the shared mobile preset:
    /// its untouched Scenarios take the shared routing. Later revisions only add
    /// what is missing, exactly as on iPhone.
    /// `ruleSetHistory` (keyed by the imported rule IDs) lists the content earlier
    /// revisions shipped for rules the preset reworked: such a rule takes the new
    /// content only while it still equals one of them, so the user's edits stay.
    /// `scenarioHistory` does the same for the routing (default exit and rule
    /// bindings) of Scenarios the preset reworked; names, icons and Wi-Fi stay.
    static func addingMobileSeed(_ incoming: Profile, named name: String, id: String,
                                 to original: ProfileLibrary, aligningLegacySeed: Bool = false,
                                 ruleSetHistory: [String: [ShippedRuleContent]] = [:],
                                 scenarioHistory: [String: [ShippedScenarioRouting]] = [:]) throws -> ProfileLibrary {
        var library = original
        let existing = library.profiles.first(where: { $0.id == id })
        // A seed inserted beside other Profiles may have had colliding IDs scoped.
        let scoping = existing?.resourceIDs ?? [:]
        let incoming = ProfileLibrary.remap(incoming, using: scoping)
        let history = Dictionary(ruleSetHistory.map { (scoping[$0.key] ?? $0.key, $0.value) }, uniquingKeysWith: { a, _ in a })
        if !library.profiles.contains(where: { $0.id == id }) {
            library.profiles.append(ProfileRecord(id: id, name: name, profile: Profile()))
        }
        var draft = library.snapshot()
        var lineIDs: [String: String] = [:]
        for line in incoming.lines where line.type == "vpn" && !line.vpnServer.isEmpty {
            if !draft.lines.contains(where: { $0.id == line.id }),
               let saved = draft.lines.first(where: { $0.enabled && $0.type == "vpn" &&
                   $0.vpnServer.caseInsensitiveCompare(line.vpnServer) == .orderedSame }) {
                lineIDs[line.id] = saved.id
            }
        }
        let seed = ProfileLibrary.remap(incoming, using: lineIDs)
        for line in seed.lines where !draft.lines.contains(where: { $0.id == line.id }) {
            draft.lines.append(line)
        }
        for rule in seed.ruleSets where !draft.ruleSets.contains(where: { $0.id == rule.id }) {
            draft.ruleSets.append(rule)
        }
        for rule in seed.ruleSets {
            guard let shipped = history[rule.id], let index = draft.ruleSets.firstIndex(where: { $0.id == rule.id }) else {
                continue
            }
            let current = ShippedRuleContent(draft.ruleSets[index])
            guard current != ShippedRuleContent(rule) else { continue }
            if shipped.contains(current) {
                draft.ruleSets[index].domains = rule.domains
                draft.ruleSets[index].cidrs = rule.cidrs
            } else {
                appLog("seed: \(name) kept the user's edits to rule \(rule.id)")
            }
        }
        // Scenarios added later follow the names the Profile's own Scenarios use: a seed that
        // joined beside other Profiles shows "<name> · <Scenario>" (see appendTemplates).
        // Decided before alignment renames anything.
        let prefixed = seedScenariosArePrefixed(draft.scenarios, baseline: existing?.baseline, prefix: name + " · ",
                                                owned: { library.scenarioSources[$0] == id },
                                                besideOthers: library.profiles.contains { $0.id != id })
        if aligningLegacySeed, let previous = existing?.baseline {
            draft.scenarios = refreshedSeedScenarios(draft.scenarios, previous: previous, seed: seed,
                                                     owned: { library.scenarioSources[$0] == id }, prefix: name + " · ")
            for note in keptCustomizations(draft.scenarios, seed: seed, prefix: name + " · ") {
                appLog("seed: \(name) kept the user's change: \(note)")
            }
        }
        // Shipped routing follows the same scoping and line reuse as the seed itself.
        let routingHistory = Dictionary(scenarioHistory.map { key, versions in
            (scoping[key] ?? key, versions.map { $0.mapped(lines: scoping, rules: scoping).mapped(lines: lineIDs) })
        }, uniquingKeysWith: { a, _ in a })
        for source in seed.scenarios {
            guard let shipped = routingHistory[source.id], library.scenarioSources[source.id] == id,
                  let index = draft.scenarios.firstIndex(where: { $0.id == source.id }),
                  let target = ShippedScenarioRouting(source) else { continue }
            let current = ShippedScenarioRouting(draft.scenarios[index])
            guard current != target else { continue }
            if let current, shipped.contains(current) {
                draft.scenarios[index].defaultLineID = target.defaultLineID
                draft.scenarios[index].defaultSubscriptionID = ""
                draft.scenarios[index].bindings = target.bindings.map {
                    RuleBinding(ruleSetID: $0.ruleSetID, lineID: $0.lineID)
                }
                draft.scenarios[index].matchOrder = []
            } else {
                appLog("seed: \(name) kept the user's edits to scenario \(source.id)")
            }
        }
        var claimed = Set(draft.scenarios.flatMap(\.matchSSIDs))
        for var scene in seed.scenarios where !draft.scenarios.contains(where: { $0.id == scene.id }) {
            scene.matchSSIDs.removeAll { !claimed.insert($0).inserted }
            if prefixed { scene.name = name + " · " + scene.name }
            draft.scenarios.append(scene)
            library.scenarioSources[scene.id] = id
        }
        let order = Set(seed.scenarios.map(\.id))
        draft.scenarios = seed.scenarios.compactMap { source in draft.scenarios.first { $0.id == source.id } }
            + draft.scenarios.filter { !order.contains($0.id) }
        if draft.activeScenarioID.isEmpty { draft.activeScenarioID = seed.activeScenarioID }
        library.applyDraft(draft, editingProfileID: id)
        if let index = library.profiles.firstIndex(where: { $0.id == id }) {
            library.profiles[index].baseline = seed
        }
        return try library.validated()
    }

    /// Whether the seed's Scenarios carry the "<name> · " prefix. The names its existing
    /// Scenarios still have decide; only when the user renamed all of them does it fall
    /// back to whether other Profiles exist.
    static func seedScenariosArePrefixed(_ scenarios: [Scenario], baseline: Profile?, prefix: String,
                                         owned: (String) -> Bool, besideOthers: Bool) -> Bool {
        for scene in scenarios where owned(scene.id) {
            guard let shipped = baseline?.scenarios.first(where: { $0.id == scene.id }) else { continue }
            if scene.name == prefix + shipped.name { return true }
            if scene.name == shipped.name { return false }
        }
        return besideOthers
    }

    /// Three-way update of the Scenarios a seed created: each field takes the new
    /// seed's value only while it still equals what the previous seed shipped.
    /// Anything the user changed (name, exits, rules, icon, Wi-Fi) is kept, so
    /// applying the same seed again changes nothing.
    static func refreshedSeedScenarios(_ scenarios: [Scenario], previous: Profile, seed: Profile,
                                       owned: (String) -> Bool, prefix: String) -> [Scenario] {
        var result = scenarios
        // Wi-Fi triggers are settled together: a Scenario still on the previous seed's
        // triggers takes the new seed's, and only triggers that stay where they are
        // (the user's, or outside this seed) are reserved. Checking one Scenario at a
        // time would drop a trigger the seed moves to a Scenario processed earlier.
        let followsSeedWiFi = Set(seed.scenarios.compactMap { source -> String? in
            guard owned(source.id), let old = previous.scenarios.first(where: { $0.id == source.id }),
                  let scene = scenarios.first(where: { $0.id == source.id }),
                  Set(scene.matchSSIDs) == Set(old.matchSSIDs) else { return nil }
            return source.id
        })
        let reserved = Set(scenarios.filter { !followsSeedWiFi.contains($0.id) }.flatMap(\.matchSSIDs))
        for source in seed.scenarios {
            guard owned(source.id),
                  let old = previous.scenarios.first(where: { $0.id == source.id }),
                  let index = result.firstIndex(where: { $0.id == source.id }) else { continue }
            var scene = result[index]
            if scene.name == old.name { scene.name = source.name }
            else if scene.name == prefix + old.name { scene.name = prefix + source.name }
            if scene.defaultLineID == old.defaultLineID, scene.defaultSubscriptionID == old.defaultSubscriptionID {
                scene.defaultLineID = source.defaultLineID
                scene.defaultSubscriptionID = source.defaultSubscriptionID
            }
            if scene.bindings == old.bindings, scene.matchOrder == old.matchOrder {
                scene.bindings = source.bindings
                scene.matchOrder = source.matchOrder
            }
            if scene.iconOverride == old.iconOverride { scene.iconOverride = source.iconOverride }
            if followsSeedWiFi.contains(scene.id) {
                scene.matchSSIDs = source.matchSSIDs.filter { !reserved.contains($0) }
            }
            result[index] = scene
        }
        return result
    }

    /// What still differs from the shared preset after alignment, i.e. what the
    /// user changed, so the log explains why a Mac and an iPhone route differently.
    static func keptCustomizations(_ scenarios: [Scenario], seed: Profile, prefix: String) -> [String] {
        seed.scenarios.compactMap { source in
            guard let scene = scenarios.first(where: { $0.id == source.id }) else { return nil }
            var fields: [String] = []
            if scene.name != source.name && scene.name != prefix + source.name { fields.append("name") }
            if scene.defaultLineID != source.defaultLineID || scene.defaultSubscriptionID != source.defaultSubscriptionID {
                fields.append("default exit")
            }
            if scene.bindings != source.bindings || scene.matchOrder != source.matchOrder { fields.append("rules") }
            if scene.iconOverride != source.iconOverride { fields.append("icon") }
            if Set(scene.matchSSIDs) != Set(source.matchSSIDs) { fields.append("Wi-Fi") }
            return fields.isEmpty ? nil : "Scenario \(source.id) (\(fields.joined(separator: ", ")))"
        }
    }

    /// A newer seed overwrites the content it owns, like a subscription refresh, but
    /// keeps what only the user can supply: credentials, Scenario names and triggers
    /// claimed by other Scenarios. The active Scenario and the edited Profile stay.
    static func refreshingSeed(_ seed: Profile, profileID: String, named name: String,
                               in library: ProfileLibrary) throws -> ProfileLibrary {
        guard let old = library.profiles.first(where: { $0.id == profileID }) else {
            throw ProfileLibraryError.invalid("预置配置已被删除")
        }
        let owned = Set(library.scenarioSources.filter { $0.value == profileID }.keys)
        // Routing is replaced below; detaching it first lets the seed drop a bound rule.
        var detached = library
        if let index = detached.profiles.firstIndex(where: { $0.id == profileID }), old.baseline == nil {
            // Without a recorded baseline the seed's Scenarios would look foreign and be duplicated.
            var baseline = old.profile
            baseline.scenarios = library.scenarios.filter { owned.contains($0.id) }
            detached.profiles[index].baseline = baseline
        }
        for index in detached.scenarios.indices where owned.contains(detached.scenarios[index].id) {
            detached.scenarios[index].bindings = []
            detached.scenarios[index].matchOrder = []
        }
        var result = try detached.refreshing(profileID: profileID, incoming: seed)
        guard let index = result.profiles.firstIndex(where: { $0.id == profileID }),
              let incoming = result.profiles[index].baseline else {
            throw ProfileLibraryError.invalid("无法刷新预置配置")
        }
        for lineIndex in result.profiles[index].profile.lines.indices {
            let line = result.profiles[index].profile.lines[lineIndex]
            guard let saved = old.profile.lines.first(where: { $0.id == line.id }) else { continue }
            result.profiles[index].profile.lines[lineIndex] = line.keepingCredentials(of: saved)
        }
        let claimed = Set(library.scenarios.filter { !owned.contains($0.id) }.flatMap(\.matchSSIDs))
        for var scene in incoming.scenarios {
            scene.matchSSIDs.removeAll(where: claimed.contains)
            if let existing = result.scenarios.firstIndex(where: { $0.id == scene.id }) {
                guard owned.contains(scene.id) else { continue }
                scene.name = result.scenarios[existing].name
                scene.iconOverride = result.scenarios[existing].iconOverride
                result.scenarios[existing] = scene
            } else {
                if library.profiles.count > 1 { scene.name = name + " · " + scene.name }
                result.scenarios.append(scene)
                result.scenarioSources[scene.id] = profileID
            }
        }
        // Scenarios the seed no longer declares stay as the user's own, routing intact.
        for scene in library.scenarios where owned.contains(scene.id) && !incoming.scenarios.contains(where: { $0.id == scene.id }) {
            if let existing = result.scenarios.firstIndex(where: { $0.id == scene.id }) { result.scenarios[existing] = scene }
        }
        result.editingProfileID = library.editingProfileID
        try result.validateReferences(allowEmptyGroups: true)
        return result
    }

    private static func library(with record: ProfileRecord) throws -> ProfileLibrary {
        var library = ProfileLibrary()
        library.schemaVersion = 2
        library.groups = []; library.scenarios = []; library.activeScenarioID = ""
        library.profiles = [record]
        library.activeProfileID = record.id
        library.editingProfileID = record.id
        return try library.validated()
    }
}

/// A Profile document embedded by an enterprise build. Its file name is the
/// Profile name, as with "Import from File".
/// Matching content a preset revision shipped for a manual rule; order and case do not count.
struct ShippedRuleContent: Hashable {
    var domains: Set<String>
    var cidrs: Set<String>

    init(domains: [String], cidrs: [String]) {
        self.domains = Set(domains.map { $0.trimmingCharacters(in: .whitespaces).lowercased() })
        self.cidrs = Set(cidrs.map { $0.trimmingCharacters(in: .whitespaces).lowercased() })
    }

    init(_ rule: RuleSet) { self.init(domains: rule.domains, cidrs: rule.cidrs) }
}

struct SeedProfile: Equatable {
    let name: String
    let text: String

    var revision: Int?
    var sourceScenarioIDs: [String] = []
    var regions: [String: String] = [:]
    var sourceRuleSetIDs: [String] = []
    /// Source rule ID -> content earlier revisions shipped (rule_set_history).
    var shippedRuleContents: [String: [ShippedRuleContent]] = [:]
    var sourceLineIDs: [String] = []
    /// Source Scenario ID -> routing earlier revisions shipped, on source IDs (scenario_history).
    var shippedScenarioRoutings: [String: [ShippedScenarioRouting]] = [:]

    /// The rule history keyed by the IDs the import gave the seed's rules, matched by position.
    func ruleSetHistory(in imported: Profile) -> [String: [ShippedRuleContent]] {
        guard sourceRuleSetIDs.count == imported.ruleSets.count else { return [:] }
        let ids = Dictionary(uniqueKeysWithValues: zip(sourceRuleSetIDs, imported.ruleSets.map(\.id)))
        return Dictionary(shippedRuleContents.compactMap { key, value in ids[key].map { ($0, value) } },
                          uniquingKeysWith: { a, _ in a })
    }

    /// The Scenario history keyed and expressed in the IDs the import gave the seed, matched by position.
    func scenarioHistory(in imported: Profile) -> [String: [ShippedScenarioRouting]] {
        guard !shippedScenarioRoutings.isEmpty,
              sourceScenarioIDs.count == imported.scenarios.count,
              sourceRuleSetIDs.count == imported.ruleSets.count,
              sourceLineIDs.count == imported.lines.count else { return [:] }
        let scenarios = Dictionary(uniqueKeysWithValues: zip(sourceScenarioIDs, imported.scenarios.map(\.id)))
        let rules = Dictionary(uniqueKeysWithValues: zip(sourceRuleSetIDs, imported.ruleSets.map(\.id)))
        let lines = Dictionary(uniqueKeysWithValues: zip(sourceLineIDs, imported.lines.map(\.id)))
        return Dictionary(shippedScenarioRoutings.compactMap { key, versions in
            scenarios[key].map { ($0, versions.map { $0.mapped(lines: lines, rules: rules) }) }
        }, uniquingKeysWith: { a, _ in a })
    }

    func regionTargets(in imported: Profile) -> [String: String] {
        guard sourceScenarioIDs.count == imported.scenarios.count else { return [:] }
        let ids = Dictionary(uniqueKeysWithValues: zip(sourceScenarioIDs, imported.scenarios.map(\.id)))
        return regions.compactMapValues { ids[$0] }
    }

    var digest: String {
        if let revision { return "mobile-v1-\(revision)" }
        return SHA256.hash(data: Data(text.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    static func load(
        directory: URL? = Bundle.main.resourceURL?.appendingPathComponent("SeedProfile", isDirectory: true)
    ) throws -> SeedProfile? {
        guard let directory, FileManager.default.fileExists(atPath: directory.path) else { return nil }
        let files = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "json" }
        guard files.count == 1, let file = files.first else {
            throw ProfileLibraryError.invalid("预置配置必须恰好是一个 JSON 文件")
        }
        let data = try Data(contentsOf: file)
        let name = file.deletingPathExtension().lastPathComponent.trimmingCharacters(in: .whitespacesAndNewlines)
        guard data.count <= 4 * 1024 * 1024, let text = String(data: data, encoding: .utf8), !name.isEmpty else {
            throw ProfileLibraryError.invalid("预置配置必须为有名称的 UTF-8 文本，且不能超过 4 MiB")
        }
        if let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           root["format"] as? String == "xdial-mobile-seed" {
            guard root["schema_version"] as? Int == 1, (root["revision"] as? Int ?? 0) > 0,
                  let object = root["profile"] as? [String: Any],
                  let scenarios = object["scenarios"] as? [[String: Any]] else {
                throw ProfileLibraryError.invalid("无效的预置场景")
            }
            let ids = scenarios.compactMap { $0["id"] as? String }
            let regions: [String: String]
            if let raw = root["region_scenarios"] {
                guard let entries = raw as? [String: String] else { throw ProfileLibraryError.invalid("无效的地区对应表") }
                var normalized: [String: String] = [:]
                for (key, id) in entries {
                    let code = key.trimmingCharacters(in: .whitespaces).uppercased()
                    guard normalized.updateValue(id, forKey: code) == nil else { throw ProfileLibraryError.invalid("重复的地区") }
                }
                regions = normalized
            } else { regions = [:] }
            guard ids.count == scenarios.count, ids.allSatisfy({ !$0.isEmpty }), Set(ids).count == ids.count,
                  regions.allSatisfy({ (key, id) in
                      (key == "*" || (key.count == 2 && key.allSatisfy { $0.isASCII && $0.isLetter })) && ids.contains(id)
                  }) else { throw ProfileLibraryError.invalid("无效的地区与场景对应表") }
            let rules = (object["rule_sets"] as? [[String: Any]]) ?? []
            let ruleIDs = rules.compactMap { $0["id"] as? String }
            var shipped: [String: [ShippedRuleContent]] = [:]
            if let raw = root["rule_set_history"] {
                guard let entries = raw as? [String: [[String: Any]]] else { throw ProfileLibraryError.invalid("无效的规则历史") }
                for (ruleID, versions) in entries {
                    guard rules.contains(where: { $0["id"] as? String == ruleID && $0["type"] as? String == "manual" }) else {
                        throw ProfileLibraryError.invalid("规则历史指向不存在的手动规则")
                    }
                    shipped[ruleID] = try versions.map { version in
                        guard Set(version.keys).isSubset(of: ["domains", "cidrs"]),
                              let domains = (version["domains"] ?? [String]()) as? [String],
                              let cidrs = (version["cidrs"] ?? [String]()) as? [String] else {
                            throw ProfileLibraryError.invalid("无效的规则历史")
                        }
                        return ShippedRuleContent(domains: domains, cidrs: cidrs)
                    }
                }
            }
            let lines = (object["lines"] as? [[String: Any]]) ?? []
            let lineIDs = lines.compactMap { $0["id"] as? String }
            var routings: [String: [ShippedScenarioRouting]] = [:]
            if let raw = root["scenario_history"] {
                guard let entries = raw as? [String: [Any]] else { throw ProfileLibraryError.invalid("无效的场景历史") }
                for (scenarioID, versions) in entries {
                    guard ids.contains(scenarioID) else { throw ProfileLibraryError.invalid("场景历史指向不存在的场景") }
                    routings[scenarioID] = try versions.map { version in
                        guard let routing = ShippedScenarioRouting.decode(version),
                              lineIDs.contains(routing.defaultLineID),
                              routing.bindings.allSatisfy({ ruleIDs.contains($0.ruleSetID) && lineIDs.contains($0.lineID) }) else {
                            throw ProfileLibraryError.invalid("无效的场景历史")
                        }
                        return routing
                    }
                }
            }
            let profile = try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
            return SeedProfile(name: name, text: String(decoding: profile, as: UTF8.self),
                               revision: root["revision"] as? Int, sourceScenarioIDs: ids, regions: regions,
                               sourceRuleSetIDs: ruleIDs.count == rules.count ? ruleIDs : [],
                               shippedRuleContents: shipped,
                               sourceLineIDs: lineIDs.count == lines.count ? lineIDs : [],
                               shippedScenarioRoutings: routings)
        }
        return SeedProfile(name: name, text: text)
    }
}

/// Which library Profile each seed became and which content it last applied.
/// It lives beside the shared library rather than inside it, because older
/// versions rewrite the library without fields they do not know.
struct SeedProfileLedger: Codable, Equatable {
    struct Entry: Codable, Equatable {
        var profileID: String
        var digest: String
        var regionTargets: [String: String]?

        func isNewer(than seed: SeedProfile) -> Bool {
            let prefix = "mobile-v1-"
            guard let revision = seed.revision, digest.hasPrefix(prefix),
                  let applied = Int(digest.dropFirst(prefix.count)) else { return false }
            return applied > revision
        }
    }

    var entries: [String: Entry] = [:]

    static func url(directory: URL = ConfigurationStorage.directory()) -> URL {
        directory.appendingPathComponent("seed-profiles.json")
    }

    static func load(from url: URL = url()) throws -> SeedProfileLedger {
        guard FileManager.default.fileExists(atPath: url.path) else { return SeedProfileLedger() }
        return try JSONDecoder().decode(SeedProfileLedger.self, from: Data(contentsOf: url))
    }

    func save(to url: URL = url()) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        try JSONEncoder().encode(self).write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }
}

extension Line {
    /// Seeds never carry secrets; a value the user saved survives a seed refresh.
    /// 用户提供的账号密码字段：预置配置刷新时保留，编辑界面对用户开放。
    static let credentialFields: [WritableKeyPath<Line, String>] = [
        \.vpnUsername, \.vpnPassword, \.trojanPassword, \.ssPassword, \.vmessUUID, \.vlessUUID, \.anytlsPassword,
    ]

    func keepingCredentials(of saved: Line) -> Line {
        var line = self
        func keep(_ field: WritableKeyPath<Line, String>) {
            if line[keyPath: field].isEmpty { line[keyPath: field] = saved[keyPath: field] }
        }
        for field in Line.credentialFields {
            keep(field)
        }
        return line
    }
}
