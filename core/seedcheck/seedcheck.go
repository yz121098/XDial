// Package seedcheck compares a company preset ("seed") by meaning rather than
// by bytes. The private packaging repository keeps one authoritative seed; each
// platform package carries either that file or a document generated from it.
// Check proves a built package routes exactly like the authority: the same
// lines, rules in the same priority, the same default exits and bindings, the
// same region-to-Scenario map and the same offline rule files. Only
// differences a platform declares as capability limits are tolerated.
package seedcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/kafeifei/xdial/core/config"
)

// Namespace is fixed so imported identities are stable between files.
const Namespace = "seed"

// Seed is a preset loaded into the shared model.
type Seed struct {
	Profile  *config.Profile
	Regions  map[string]string // region code or "*" -> imported Scenario ID
	Revision int
	Mobile   bool
	// RuleHistory: imported rule ID -> matching content earlier revisions
	// shipped (rule_set_history). Apps update such a rule to the current
	// content only while it still equals one of these.
	RuleHistory map[string][]RuleContent
	// ScenarioHistory: imported Scenario ID -> routing earlier revisions
	// shipped (scenario_history), in imported line and rule IDs. Apps replace
	// such a Scenario's routing only while it still equals one of these.
	ScenarioHistory map[string][]ScenarioRouting
}

// sourceItem is the identity of a line, rule or Scenario as the seed file names it.
type sourceItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// ScenarioRouting is the routing a preset revision shipped for a Scenario:
// the default exit and the ordered rule bindings. Order counts, since the
// first matching rule decides.
type ScenarioRouting struct {
	DefaultLineID string           `json:"default_line_id"`
	Bindings      []RoutingBinding `json:"bindings"`
}

// RoutingBinding is one shipped rule binding of a ScenarioRouting.
type RoutingBinding struct {
	RuleSetID string `json:"rule_set_id"`
	LineID    string `json:"line_id"`
}

// RuleContent is a manual rule's matching content; order and case do not count.
type RuleContent struct {
	Domains []string `json:"domains"`
	CIDRs   []string `json:"cidrs"`
}

func (c RuleContent) key() string {
	normalize := func(values []string) []string {
		out := make([]string, 0, len(values))
		seen := map[string]bool{}
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
		}
		sort.Strings(out)
		return out
	}
	data, _ := json.Marshal([][]string{normalize(c.Domains), normalize(c.CIDRs)})
	return string(data)
}

// Load accepts the mobile seed envelope ("xdial-mobile-seed") or a plain
// portable profile document, as both app importers do.
func Load(data []byte) (*Seed, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("seed is not JSON")
	}
	var format string
	_ = json.Unmarshal(root["format"], &format)
	if format != "xdial-mobile-seed" {
		profile, err := config.ImportProfileDocument(data, Namespace)
		if err != nil {
			return nil, fmt.Errorf("import profile document: %v", err)
		}
		return &Seed{Profile: profile}, nil
	}
	var envelope struct {
		SchemaVersion int                                     `json:"schema_version"`
		Revision      int                                     `json:"revision"`
		Regions       map[string]string                       `json:"region_scenarios"`
		Profile       json.RawMessage                         `json:"profile"`
		RuleHistory   map[string][]map[string]json.RawMessage `json:"rule_set_history"`
		ScenarioHist  map[string][]json.RawMessage            `json:"scenario_history"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.SchemaVersion != 1 || envelope.Revision <= 0 {
		return nil, fmt.Errorf("invalid mobile seed envelope")
	}
	var source struct {
		Scenarios []sourceItem `json:"scenarios"`
		RuleSets  []sourceItem `json:"rule_sets"`
		Lines     []sourceItem `json:"lines"`
	}
	if err := json.Unmarshal(envelope.Profile, &source); err != nil {
		return nil, fmt.Errorf("invalid seed profile")
	}
	if err := requireMobileCompatible(envelope.Profile); err != nil {
		return nil, err
	}
	profile, err := config.ImportProfileDocument(envelope.Profile, Namespace)
	if err != nil {
		return nil, fmt.Errorf("import seed profile: %v", err)
	}
	if len(source.Scenarios) != len(profile.Scenarios) {
		return nil, fmt.Errorf("seed Scenarios changed during import")
	}
	ids := map[string]string{}
	for i, scenario := range source.Scenarios {
		ids[scenario.ID] = profile.Scenarios[i].ID
	}
	regions := map[string]string{}
	for key, scenario := range envelope.Regions {
		target, ok := ids[scenario]
		if !ok {
			return nil, fmt.Errorf("region %s names unknown Scenario %s", key, scenario)
		}
		code := strings.ToUpper(strings.TrimSpace(key))
		if code != "*" && !(len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z') {
			return nil, fmt.Errorf("region %q must be * or a two-letter country code", key)
		}
		if _, repeated := regions[code]; repeated {
			return nil, fmt.Errorf("region %s appears twice", code)
		}
		regions[code] = target
	}
	if raw, present := root["rule_set_history"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("rule_set_history must be an object")
	}
	history := map[string][]RuleContent{}
	if len(envelope.RuleHistory) > 0 && len(source.RuleSets) != len(profile.RuleSets) {
		return nil, fmt.Errorf("seed rule sets changed during import")
	}
	for ruleID, versions := range envelope.RuleHistory {
		index := -1
		for i, rule := range source.RuleSets {
			if rule.ID == ruleID && rule.Type == "manual" {
				index = i
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("rule_set_history names %s, which is not a manual rule of the preset", ruleID)
		}
		if versions == nil {
			return nil, fmt.Errorf("rule_set_history for %s must be an array", ruleID)
		}
		for _, version := range versions {
			if version == nil {
				return nil, fmt.Errorf("rule_set_history for %s must contain objects", ruleID)
			}
			var content RuleContent
			for key, value := range version {
				target := map[string]*[]string{"domains": &content.Domains, "cidrs": &content.CIDRs}[key]
				if target == nil {
					return nil, fmt.Errorf("rule_set_history for %s may only list domains and cidrs", ruleID)
				}
				// Unlike Go's []string decoder, the Apps reject null arrays
				// and null elements rather than treating them as empty values.
				var values []*string
				if json.Unmarshal(value, &values) != nil || values == nil {
					return nil, fmt.Errorf("rule_set_history for %s requires string arrays", ruleID)
				}
				for _, item := range values {
					if item == nil {
						return nil, fmt.Errorf("rule_set_history for %s requires strings", ruleID)
					}
					*target = append(*target, *item)
				}
			}
			history[profile.RuleSets[index].ID] = append(history[profile.RuleSets[index].ID], content)
		}
	}
	scenarioHistory, err := loadScenarioHistory(root, envelope.ScenarioHist, source.Scenarios, source.RuleSets, source.Lines, profile)
	if err != nil {
		return nil, err
	}
	return &Seed{Profile: profile, Regions: regions, Revision: envelope.Revision, Mobile: true,
		RuleHistory: history, ScenarioHistory: scenarioHistory}, nil
}

// loadScenarioHistory validates scenario_history as strictly as the Apps do and
// expresses it in the imported IDs, matched by position like the rest of the seed.
func loadScenarioHistory(
	root map[string]json.RawMessage, entries map[string][]json.RawMessage,
	scenarios, rules, lines []sourceItem, profile *config.Profile,
) (map[string][]ScenarioRouting, error) {
	if raw, present := root["scenario_history"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("scenario_history must be an object")
	}
	if len(entries) == 0 {
		return map[string][]ScenarioRouting{}, nil
	}
	if len(rules) != len(profile.RuleSets) || len(lines) != len(profile.Lines) {
		return nil, fmt.Errorf("seed lines or rule sets changed during import")
	}
	scenarioIDs, ruleIDs, lineIDs := map[string]string{}, map[string]string{}, map[string]string{}
	for i, scenario := range scenarios {
		scenarioIDs[scenario.ID] = profile.Scenarios[i].ID
	}
	for i, rule := range rules {
		ruleIDs[rule.ID] = profile.RuleSets[i].ID
	}
	for i, line := range lines {
		lineIDs[line.ID] = profile.Lines[i].ID
	}
	history := map[string][]ScenarioRouting{}
	for scenarioID, versions := range entries {
		target, ok := scenarioIDs[scenarioID]
		if !ok {
			return nil, fmt.Errorf("scenario_history names %s, which is not a Scenario of the preset", scenarioID)
		}
		if versions == nil {
			return nil, fmt.Errorf("scenario_history for %s must be an array", scenarioID)
		}
		for _, raw := range versions {
			routing, err := decodeScenarioRouting(raw)
			if err != nil {
				return nil, fmt.Errorf("scenario_history for %s: %v", scenarioID, err)
			}
			mapped := ScenarioRouting{DefaultLineID: lineIDs[routing.DefaultLineID], Bindings: []RoutingBinding{}}
			if mapped.DefaultLineID == "" {
				return nil, fmt.Errorf("scenario_history for %s names unknown line %s", scenarioID, routing.DefaultLineID)
			}
			for _, binding := range routing.Bindings {
				rule, line := ruleIDs[binding.RuleSetID], lineIDs[binding.LineID]
				if rule == "" || line == "" {
					return nil, fmt.Errorf("scenario_history for %s binds unknown rule %s or line %s",
						scenarioID, binding.RuleSetID, binding.LineID)
				}
				mapped.Bindings = append(mapped.Bindings, RoutingBinding{RuleSetID: rule, LineID: line})
			}
			history[target] = append(history[target], mapped)
		}
	}
	return history, nil
}

// decodeScenarioRouting admits only default_line_id and bindings of
// rule_set_id / line_id strings, rejecting nulls as the Apps do.
func decodeScenarioRouting(raw json.RawMessage) (ScenarioRouting, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return ScenarioRouting{}, fmt.Errorf("each version must be an object")
	}
	var routing ScenarioRouting
	for key, value := range object {
		switch key {
		case "default_line_id":
			if json.Unmarshal(value, &routing.DefaultLineID) != nil || routing.DefaultLineID == "" {
				return ScenarioRouting{}, fmt.Errorf("default_line_id must be a non-empty string")
			}
		case "bindings":
			var bindings []map[string]json.RawMessage
			if json.Unmarshal(value, &bindings) != nil || bindings == nil {
				return ScenarioRouting{}, fmt.Errorf("bindings must be an array of objects")
			}
			for _, binding := range bindings {
				var item RoutingBinding
				if len(binding) != 2 || json.Unmarshal(binding["rule_set_id"], &item.RuleSetID) != nil ||
					json.Unmarshal(binding["line_id"], &item.LineID) != nil || item.RuleSetID == "" || item.LineID == "" {
					return ScenarioRouting{}, fmt.Errorf("each binding needs exactly rule_set_id and line_id strings")
				}
				routing.Bindings = append(routing.Bindings, item)
			}
		default:
			return ScenarioRouting{}, fmt.Errorf("a version may only list default_line_id and bindings")
		}
	}
	if routing.DefaultLineID == "" {
		return ScenarioRouting{}, fmt.Errorf("default_line_id is required")
	}
	return routing, nil
}

// A shared preset may only use what the phone's importer reads the same way
// (macos/Sources/XDial/Models.swift, shared by the Apple hosts, and
// MobileConfigurationService.validate in ios/XDialIOS): the Mac imports through
// Go, which accepts more, and comparing two identical files through one loader
// cannot reveal what the phone drops or rejects. Anything else is refused here
// instead of being certified as equivalent.
var (
	mobileProfileKeys  = keySet("lines", "rule_sets", "scenarios", "subscriptions", "active_scenario_id", "tailscale")
	mobileRequiredKeys = []string{"lines", "rule_sets", "subscriptions", "scenarios", "active_scenario_id"}
	mobileLineKeys     = keySet("id", "name", "type", "enabled", "verified", "allow_insecure",
		"vpn_server", "vpn_username", "vpn_password", "trojan_server", "trojan_port", "trojan_password", "trojan_sni",
		"ss_server", "ss_port", "ss_method", "ss_password", "vmess_server", "vmess_port", "vmess_uuid", "vmess_alt_id",
		"tailscale_exit_node", "tailscale_magic_dns", "tailscale_authenticated")
	// The phone also reads "ss" (rewritten to "shadowsocks"); the Go importer does not.
	mobileLineTypes     = keySet("direct", "vpn", "trojan", "shadowsocks", "vmess", "tailscale")
	mobileRuleKeys      = keySet("id", "name", "type", "enabled", "url", "format", "domains", "cidrs")
	mobileRuleTypes     = keySet("url", "manual")
	mobileRuleFormats   = keySet("", "auto", "srs", "json", "text")
	mobileScenarioKeys  = keySet("id", "name", "icon", "match_ssids", "bindings", "default_line_id", "default_subscription_id")
	mobileBindingKeys   = keySet("rule_set_id", "line_id", "subscription_id")
	mobileTailscaleKeys = keySet("hostname")

	// Keys the phone's decoders cannot do without: Line, Scenario and RuleBinding
	// decode these with decode(); RuleSet uses synthesized Decodable, which needs
	// every stored property despite their initial values.
	mobileLineRequired     = []string{"id", "name", "type"}
	mobileRuleRequired     = []string{"id", "name", "type", "enabled", "url", "format", "domains", "cidrs"}
	mobileScenarioRequired = []string{"id", "name"}
	mobileBindingRequired  = []string{"rule_set_id"}
)

// platformDefaults are fields whose omission the platforms read differently:
// the phone's decoder fills these values, while the Go importer leaves zero
// values (a Line would be disabled, a port 0), so a shared preset must state them.
var platformDefaults = map[string][]string{
	"":            {"enabled"},
	"trojan":      {"trojan_port"},
	"shadowsocks": {"ss_port", "ss_method"},
	"vmess":       {"vmess_port"},
}

func keySet(keys ...string) map[string]bool {
	set := map[string]bool{}
	for _, key := range keys {
		set[key] = true
	}
	return set
}

type rawObject = map[string]json.RawMessage

func text(object rawObject, key string) string {
	var value string
	_ = json.Unmarshal(object[key], &value)
	return value
}

func onlyKeys(object rawObject, allowed map[string]bool, what string) error {
	for key := range object {
		if !allowed[key] {
			return fmt.Errorf("%s uses %s, which the phone does not read", what, key)
		}
	}
	return nil
}

func requireKeys(object rawObject, keys []string, what string) error {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("%s must set %s: the phone requires it", what, key)
		}
	}
	return nil
}

// uniqueIDs mirrors the phone's identifier check: present, not blank, not repeated.
func uniqueIDs(objects []rawObject, key, kind string) (map[string]bool, error) {
	ids := map[string]bool{}
	for _, object := range objects {
		id := text(object, key)
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("a %s has no id", kind)
		}
		if ids[id] {
			return nil, fmt.Errorf("%s %s appears twice", kind, id)
		}
		ids[id] = true
	}
	return ids, nil
}

// remoteHTTPS mirrors the phone's rule address check.
func remoteHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasSuffix(host, ".localhost") && !strings.HasSuffix(host, ".local")
}

// requireMobileCompatible accepts a shared preset only if the phone decodes and
// validates it to the same meaning the Go importer gives the Mac
// (Models.swift decoders, MobileConfigurationService.importProfile/validate).
func requireMobileCompatible(profile json.RawMessage) error {
	var top rawObject
	var document struct {
		Lines         []rawObject `json:"lines"`
		RuleSets      []rawObject `json:"rule_sets"`
		Scenarios     []rawObject `json:"scenarios"`
		Subscriptions []rawObject `json:"subscriptions"`
		Tailscale     rawObject   `json:"tailscale"`
	}
	if json.Unmarshal(profile, &top) != nil || json.Unmarshal(profile, &document) != nil {
		return fmt.Errorf("invalid seed profile")
	}
	if err := onlyKeys(top, mobileProfileKeys, "the preset"); err != nil {
		return err
	}
	if err := requireKeys(top, mobileRequiredKeys, "the preset"); err != nil {
		return err
	}
	if len(document.Subscriptions) > 0 {
		return fmt.Errorf("the preset must not carry subscriptions: their routing is not compared across platforms")
	}
	if err := onlyKeys(document.Tailscale, mobileTailscaleKeys, "the preset's tailscale identity"); err != nil {
		return err
	}
	lineIDs, err := uniqueIDs(document.Lines, "id", "line")
	if err != nil {
		return err
	}
	ruleIDs, err := uniqueIDs(document.RuleSets, "id", "rule set")
	if err != nil {
		return err
	}
	scenarioIDs, err := uniqueIDs(document.Scenarios, "id", "Scenario")
	if err != nil {
		return err
	}
	for _, line := range document.Lines {
		what := "line " + text(line, "id")
		if err := onlyKeys(line, mobileLineKeys, what); err != nil {
			return err
		}
		if err := requireKeys(line, mobileLineRequired, what); err != nil {
			return err
		}
		kind := text(line, "type")
		if !mobileLineTypes[kind] {
			return fmt.Errorf("%s has type %q, which the phone does not support", what, kind)
		}
		for _, field := range append(append([]string{}, platformDefaults[""]...), platformDefaults[kind]...) {
			if _, ok := line[field]; !ok {
				return fmt.Errorf("%s must set %s: platforms default it differently", what, field)
			}
		}
	}
	for _, rule := range document.RuleSets {
		what := "rule set " + text(rule, "id")
		if err := onlyKeys(rule, mobileRuleKeys, what); err != nil {
			return err
		}
		if err := requireKeys(rule, mobileRuleRequired, what); err != nil {
			return err
		}
		kind := text(rule, "type")
		if !mobileRuleTypes[kind] {
			return fmt.Errorf("%s has type %q, which the phone does not support", what, kind)
		}
		if format := text(rule, "format"); !mobileRuleFormats[format] {
			return fmt.Errorf("%s has format %q, which the phone does not support", what, format)
		}
		var enabled bool
		_ = json.Unmarshal(rule["enabled"], &enabled)
		address := text(rule, "url")
		if kind == "url" && ((enabled && strings.TrimSpace(address) == "") || (address != "" && !remoteHTTPS(address))) {
			return fmt.Errorf("%s needs a remote HTTPS address", what)
		}
	}
	for _, scenario := range document.Scenarios {
		what := "Scenario " + text(scenario, "id")
		if err := onlyKeys(scenario, mobileScenarioKeys, what); err != nil {
			return err
		}
		if err := requireKeys(scenario, mobileScenarioRequired, what); err != nil {
			return err
		}
		// Without subscriptions, the one default exit the phone requires is a line.
		if line := text(scenario, "default_line_id"); !lineIDs[line] || text(scenario, "default_subscription_id") != "" {
			return fmt.Errorf("%s needs one default exit naming a line of the preset", what)
		}
		var bindings []rawObject
		if err := json.Unmarshal(scenario["bindings"], &bindings); err != nil && len(scenario["bindings"]) > 0 {
			return fmt.Errorf("%s has invalid bindings", what)
		}
		if _, err := uniqueIDs(bindings, "rule_set_id", what+" binding"); err != nil {
			return err
		}
		for _, binding := range bindings {
			if err := onlyKeys(binding, mobileBindingKeys, what+" binding"); err != nil {
				return err
			}
			if err := requireKeys(binding, mobileBindingRequired, what+" binding"); err != nil {
				return err
			}
			if !ruleIDs[text(binding, "rule_set_id")] || !lineIDs[text(binding, "line_id")] ||
				text(binding, "subscription_id") != "" {
				return fmt.Errorf("%s binds %s to something the preset does not define", what, text(binding, "rule_set_id"))
			}
		}
	}
	active := text(top, "active_scenario_id")
	if (len(document.Scenarios) > 0 || active != "") && !scenarioIDs[active] {
		return fmt.Errorf("the preset's active_scenario_id must name one of its Scenarios")
	}
	return nil
}

// LegacyProfileDocument is what a desktop app without mobile-seed support
// receives: the authority's profile object, unchanged, so it imports to the
// same identities and routing.
func LegacyProfileDocument(authority []byte) ([]byte, error) {
	var envelope struct {
		Format  string          `json:"format"`
		Profile json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(authority, &envelope); err != nil || envelope.Format != "xdial-mobile-seed" {
		return nil, fmt.Errorf("authority must be a mobile seed")
	}
	var value any
	if err := json.Unmarshal(envelope.Profile, &value); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(value, "", "  ")
	return append(out, '\n'), err
}

func normalizedFormat(format string) string {
	switch strings.ToLower(format) {
	case "srs", "binary":
		return "binary"
	case "json", "source":
		return "source"
	case "", "auto":
		return "auto"
	}
	return format
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// withoutSecrets clears the user's account fields before a line is serialized,
// so credentials never reach a fingerprint that the tool prints or compares.
func withoutSecrets(line config.Line) config.Line {
	line.VPNUsername, line.VPNPassword, line.TrojanPassword, line.SSPass = "", "", "", ""
	line.VMessUUID, line.VLESSUUID, line.AnyTLSPassword, line.TailscaleAuthKey = "", "", "", ""
	return line
}

// Fingerprint is the routing meaning of a seed. Map keys name the parts a
// platform may declare as unsupported: "lines", "rule_sets", "scenarios",
// "active_scenario", "regions", "offline_rules", "rule_set_history",
// "scenario_history".
func Fingerprint(seed *Seed, offline map[string]string) map[string]any {
	p := seed.Profile
	var lines []map[string]any
	for _, line := range p.Lines {
		var object map[string]any
		data, _ := json.Marshal(withoutSecrets(line))
		_ = json.Unmarshal(data, &object)
		for key, value := range object {
			// Empty values are formatting, not meaning. A Direct line has no
			// protocol options, so only what it is counts.
			direct := line.Type == "direct" && key != "id" && key != "name" && key != "type" && key != "enabled"
			if direct || strings.HasPrefix(key, "identity_") || key == "verified" || isEmpty(value) {
				delete(object, key)
			}
		}
		lines = append(lines, object)
	}
	var rules []map[string]any
	for _, rule := range p.RuleSets {
		rules = append(rules, ruleFingerprint(rule))
	}
	var scenarios []map[string]any
	for _, scenario := range p.Scenarios {
		var bindings []map[string]any
		for _, binding := range scenario.Bindings {
			bindings = append(bindings, map[string]any{"rule_set_id": binding.RuleSetID, "line_id": binding.LineID,
				"conditions": sorted(binding.ConditionIDs)})
		}
		scenarios = append(scenarios, map[string]any{
			"id": scenario.ID, "name": scenario.Name, "default_line_id": scenario.DefaultLineID,
			"match_ssids": sorted(scenario.MatchSSIDs), "bindings": bindings,
			"match_order": append([]string{}, scenario.MatchOrder...),
		})
	}
	regions := map[string]any{}
	for key, value := range seed.Regions {
		regions[key] = value
	}
	offlineRules := map[string]any{}
	for key, value := range offline {
		offlineRules[key] = value
	}
	ruleHistory := map[string]any{}
	for id, versions := range seed.RuleHistory {
		keys := []string{}
		for _, version := range versions {
			keys = append(keys, version.key())
		}
		ruleHistory[id] = sorted(keys)
	}
	scenarioHistory := map[string]any{}
	for id, versions := range seed.ScenarioHistory {
		keys := []string{}
		for _, version := range versions {
			data, _ := json.Marshal(version)
			keys = append(keys, string(data))
		}
		scenarioHistory[id] = sorted(keys)
	}
	return canonical(map[string]any{"lines": lines, "rule_sets": rules, "scenarios": scenarios,
		"active_scenario": p.ActiveScenarioID, "regions": regions, "offline_rules": offlineRules,
		"rule_set_history": ruleHistory, "scenario_history": scenarioHistory})
}

// ruleFingerprint is everything a RuleSet matches on. Conditions keep their
// order (they are evaluated in turn); a native rule counts by its JSON value,
// not its formatting.
func ruleFingerprint(rule config.RuleSet) map[string]any {
	var native any
	if len(rule.NativeRule) > 0 {
		if err := json.Unmarshal(rule.NativeRule, &native); err != nil {
			native = string(rule.NativeRule)
		}
	}
	conditions := []map[string]any{}
	for _, condition := range rule.Conditions {
		conditions = append(conditions, ruleFingerprint(condition))
	}
	return map[string]any{
		"id": rule.ID, "name": rule.Name, "type": string(rule.Type), "enabled": rule.Enabled,
		"url": rule.URL, "format": normalizedFormat(rule.Format), "fetch_line_id": rule.EffectiveFetchLineID(),
		"domains": sorted(rule.Domains), "cidrs": sorted(rule.CIDRs), "invert": rule.Invert,
		"no_resolve": rule.NoResolve, "native_rule": native, "applications": rule.Applications,
		"processes": sorted(rule.Processes), "conditions": conditions,
	}
}

func canonical(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// OfflineRules reads a RuleSets directory (manifest.json mapping rule URLs to
// files) into URL -> SHA-256 of the file. A missing directory is empty.
func OfflineRules(directory string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid rule manifest")
	}
	out := map[string]string{}
	for url, name := range manifest {
		if name != filepath.Base(name) {
			return nil, fmt.Errorf("rule manifest names a path: %s", name)
		}
		file, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("offline rule %s is missing", name)
		}
		sum := sha256.Sum256(file)
		out[url] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// Diff lists every difference between two fingerprints, except for parts the
// platform declared as unsupported. Paths read like "scenarios[1].bindings".
func Diff(authority, packaged map[string]any, allowed []string) []string {
	skip := map[string]bool{}
	for _, part := range allowed {
		skip[part] = true
	}
	var out []string
	keys := make([]string, 0, len(authority))
	for key := range authority {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if skip[key] {
			continue
		}
		out = append(out, diffValue(key, authority[key], packaged[key])...)
	}
	return out
}

func diffValue(path string, a, b any) []string {
	if reflect.DeepEqual(a, b) {
		return nil
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []string{path + ": " + describe(a) + " -> " + describe(b)}
		}
		keys := map[string]bool{}
		for key := range av {
			keys[key] = true
		}
		for key := range bv {
			keys[key] = true
		}
		names := make([]string, 0, len(keys))
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		var out []string
		for _, key := range names {
			out = append(out, diffValue(path+"."+key, av[key], bv[key])...)
		}
		return out
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return []string{fmt.Sprintf("%s: %d items -> %s", path, len(av), describe(b))}
		}
		var out []string
		for i := range av {
			out = append(out, diffValue(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
		return out
	}
	return []string{path + ": " + describe(a) + " -> " + describe(b)}
}

func describe(value any) string {
	if value == nil {
		return "missing"
	}
	if list, ok := value.([]any); ok {
		return fmt.Sprintf("%d items", len(list))
	}
	data, _ := json.Marshal(value)
	if len(data) > 80 {
		return string(data[:77]) + "..."
	}
	return string(data)
}

func isEmpty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}
