package seedcheck

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kafeifei/xdial/core/config"
)

const authority = `{"format":"xdial-mobile-seed","schema_version":1,"revision":2,
 "region_scenarios":{"cn":"domestic","*":"overseas"},
 "profile":{"lines":[{"id":"direct","name":"直连","type":"direct","enabled":true},
  {"id":"vpn","name":"公司 VPN","type":"vpn","enabled":true,"vpn_server":"vpn.example.com:8443"}],
  "rule_sets":[{"id":"intranet","name":"公司内网","type":"manual","enabled":true,"url":"","format":"auto","domains":["example.com"],"cidrs":[]},
   {"id":"cn","name":"国内网站","type":"url","enabled":true,"format":"srs","url":"https://rules.example/cn.srs","domains":[],"cidrs":[]}],
  "scenarios":[{"id":"domestic","name":"在国内","default_line_id":"vpn","bindings":[{"rule_set_id":"intranet","line_id":"vpn"},{"rule_set_id":"cn","line_id":"direct"}]},
   {"id":"overseas","name":"在国外","default_line_id":"direct","bindings":[{"rule_set_id":"cn","line_id":"vpn"},{"rule_set_id":"intranet","line_id":"vpn"}]}],
  "subscriptions":[],"active_scenario_id":"domestic"}}`

// document is the authority's plain profile document: what an app's importer
// reads, so app-side values the shared preset may not carry can be tested.
func document(t *testing.T, seed string) string {
	t.Helper()
	data, err := LegacyProfileDocument([]byte(seed))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustLoad(t *testing.T, text string) *Seed {
	t.Helper()
	seed, err := Load([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestLegacyDocumentRoutesLikeTheAuthorityExceptDeclaredParts(t *testing.T) {
	want := Fingerprint(mustLoad(t, authority), map[string]string{"https://rules.example/cn.srs": "abc"})
	legacy, err := LegacyProfileDocument([]byte(authority))
	if err != nil {
		t.Fatal(err)
	}
	got := Fingerprint(mustLoad(t, string(legacy)), nil)
	if d := Diff(want, got, []string{"regions", "offline_rules"}); len(d) != 0 {
		t.Fatalf("legacy document changes routing: %v", d)
	}
	if d := Diff(want, got, nil); len(d) == 0 {
		t.Fatal("undeclared differences in regions and offline rules must be reported")
	}
}

func TestDiffReportsEveryRoutingChange(t *testing.T) {
	want := Fingerprint(mustLoad(t, authority), nil)
	cases := map[string]string{
		"default exit": strings.Replace(authority, `"name":"在国内","default_line_id":"vpn"`, `"name":"在国内","default_line_id":"direct"`, 1),
		"rule priority": strings.Replace(authority,
			`"bindings":[{"rule_set_id":"intranet","line_id":"vpn"},{"rule_set_id":"cn","line_id":"direct"}]`,
			`"bindings":[{"rule_set_id":"cn","line_id":"direct"},{"rule_set_id":"intranet","line_id":"vpn"}]`, 1),
		"bound line":     strings.Replace(authority, `{"rule_set_id":"cn","line_id":"direct"}`, `{"rule_set_id":"cn","line_id":"vpn"}`, 1),
		"region mapping": strings.Replace(authority, `"cn":"domestic"`, `"cn":"overseas"`, 1),
		"rule content":   strings.Replace(authority, `"domains":["example.com"]`, `"domains":["example.org"]`, 1),
		"scenario name":  strings.Replace(authority, `"name":"在国外"`, `"name":"国外"`, 1),
	}
	for name, changed := range cases {
		got := Fingerprint(mustLoad(t, changed), nil)
		if d := Diff(want, got, nil); len(d) == 0 {
			t.Errorf("%s change not detected", name)
		}
	}
	offline := Fingerprint(mustLoad(t, authority), map[string]string{"https://rules.example/cn.srs": "other"})
	if d := Diff(Fingerprint(mustLoad(t, authority), map[string]string{"https://rules.example/cn.srs": "abc"}), offline, nil); len(d) != 1 {
		t.Errorf("offline rule digest change not reported exactly once: %v", d)
	}
}

func TestSecretsAndFormattingDoNotCount(t *testing.T) {
	want := Fingerprint(mustLoad(t, authority), nil)
	withPassword := strings.Replace(authority, `"vpn_server":"vpn.example.com:8443"`, `"vpn_server":"vpn.example.com:8443","vpn_password":"x"`, 1)
	if d := Diff(want, Fingerprint(mustLoad(t, withPassword), nil), nil); len(d) != 0 {
		t.Fatalf("a filled-in credential is not a preset difference: %v", d)
	}
	every := Fingerprint(&Seed{Profile: &config.Profile{Lines: []config.Line{{ID: "all", Type: "vpn",
		VPNUsername: "user-secret", VPNPassword: "pass-secret", TrojanPassword: "trojan-secret", SSPass: "ss-secret",
		VMessUUID: "uuid-secret", VLESSUUID: "vless-secret", AnyTLSPassword: "anytls-secret", TailscaleAuthKey: "key-secret"}}}}, nil)
	if printed, _ := json.Marshal(every); strings.Contains(string(printed), "secret") {
		t.Fatalf("fingerprint carries credentials: %s", printed)
	}
	// The desktop app writes "binary" for the phone's "srs".
	binary := strings.Replace(document(t, authority), `"format": "srs"`, `"format": "binary"`, 1)
	if !strings.Contains(binary, `"format": "binary"`) {
		t.Fatal("fixture did not change")
	}
	if d := Diff(want, Fingerprint(mustLoad(t, binary), nil), []string{"regions", "offline_rules"}); len(d) != 0 {
		t.Fatalf("format synonyms reported as differences: %v", d)
	}
}

func TestEffectiveValuesDoNotCount(t *testing.T) {
	want := Fingerprint(mustLoad(t, authority), nil)
	// An app fills defaults: Direct fetches rule sets, and a Direct line has no protocol options.
	filled := strings.Replace(authority, `"format":"srs"`, `"format":"srs","fetch_line_id":"direct"`, 1)
	filled = strings.Replace(filled, `"type":"direct","enabled":true}`, `"type":"direct","enabled":true,"anytls_port":443}`, 1)
	filled = strings.Replace(filled, `"name":"在国外","default_line_id":"direct"`, `"name":"在国外","default_line_id":"direct","match_order":[]`, 1)
	if d := Diff(want, Fingerprint(mustLoad(t, document(t, filled)), nil), []string{"regions"}); len(d) != 0 {
		t.Fatalf("effective defaults reported as differences: %v", d)
	}
	viaVPN := strings.Replace(authority, `"format":"srs"`, `"format":"srs","fetch_line_id":"vpn"`, 1)
	if d := Diff(want, Fingerprint(mustLoad(t, document(t, viaVPN)), nil), []string{"regions"}); len(d) != 1 {
		t.Fatalf("fetching rules through another line must count: %v", d)
	}
}

func TestDiffReportsMatchingContentOfEveryRuleKind(t *testing.T) {
	base := func() config.RuleSet {
		return config.RuleSet{ID: "r", Name: "r", Type: "group", Enabled: true,
			NativeRule:   json.RawMessage(`{"domain_suffix":["a.example"]}`),
			Applications: []config.ApplicationMatch{{Path: "/Applications/A.app", BundleIdentifier: "com.example.a"}},
			Processes:    []string{"a"},
			Conditions:   []config.RuleSet{{ID: "c", Type: "manual", Enabled: true, Domains: []string{"c.example"}}}}
	}
	fingerprint := func(rule config.RuleSet) map[string]any {
		return Fingerprint(&Seed{Profile: &config.Profile{RuleSets: []config.RuleSet{rule}}}, nil)
	}
	want := fingerprint(base())
	reformatted := base()
	reformatted.NativeRule = json.RawMessage("{ \"domain_suffix\": [ \"a.example\" ] }")
	if d := Diff(want, fingerprint(reformatted), nil); len(d) != 0 {
		t.Fatalf("native rule formatting reported as a difference: %v", d)
	}
	changes := map[string]func(*config.RuleSet){
		"native rule": func(r *config.RuleSet) { r.NativeRule = json.RawMessage(`{"domain_suffix":["b.example"]}`) },
		"application": func(r *config.RuleSet) { r.Applications[0].BundleIdentifier = "com.example.b" },
		"process":     func(r *config.RuleSet) { r.Processes = []string{"b"} },
		"condition":   func(r *config.RuleSet) { r.Conditions[0].Domains = []string{"d.example"} },
	}
	for name, change := range changes {
		rule := base()
		change(&rule)
		if d := Diff(want, fingerprint(rule), nil); len(d) == 0 {
			t.Errorf("%s change not detected", name)
		}
	}
}

func TestLoadRequiresFieldsPlatformsDefaultDifferently(t *testing.T) {
	cases := map[string]string{
		"line enabled": strings.Replace(authority, `"type":"vpn","enabled":true,`, `"type":"vpn",`, 1),
		"rule enabled": strings.Replace(authority, `"type":"manual","enabled":true,`, `"type":"manual",`, 1),
		"trojan port": strings.Replace(authority, `{"id":"vpn","name":"公司 VPN","type":"vpn","enabled":true,"vpn_server":"vpn.example.com:8443"}`,
			`{"id":"vpn","name":"公司 VPN","type":"trojan","enabled":true,"trojan_server":"t.example.com"}`, 1),
	}
	for name, seed := range cases {
		if seed == authority {
			t.Fatalf("%s: fixture did not change", name)
		}
		if _, err := Load([]byte(seed)); err == nil || !strings.Contains(err.Error(), "must set") {
			t.Errorf("%s omitted: got %v", name, err)
		}
	}
}

func TestLoadRejectsWhatThePhoneDoesNotRead(t *testing.T) {
	cases := map[string]string{
		"inverted rule": strings.Replace(authority, `"domains":["example.com"]`, `"domains":["example.com"],"invert":true`, 1),
		"native rule":   strings.Replace(authority, `"type":"manual","enabled":true,`, `"type":"native","enabled":true,`, 1),
		"binding conditions": strings.Replace(authority, `{"rule_set_id":"intranet","line_id":"vpn"}`,
			`{"rule_set_id":"intranet","line_id":"vpn","condition_ids":["x"]}`, 1),
		"match order":          strings.Replace(authority, `"name":"在国外",`, `"name":"在国外","match_order":["cn"],`, 1),
		"line type":            strings.Replace(authority, `"type":"vpn","enabled":true,`, `"type":"anytls","enabled":true,`, 1),
		"rule format":          strings.Replace(authority, `"format":"srs"`, `"format":"source"`, 1),
		"rule without cidrs":   strings.Replace(authority, `"domains":["example.com"],"cidrs":[]`, `"domains":["example.com"]`, 1),
		"rule without url":     strings.Replace(authority, `"enabled":true,"url":"","format":"auto"`, `"enabled":true,"format":"auto"`, 1),
		"ss alias":             strings.Replace(authority, `"type":"vpn","enabled":true,"vpn_server":"vpn.example.com:8443"`, `"type":"ss","enabled":true,"ss_port":1,"ss_method":"m"`, 1),
		"plain http rule":      strings.Replace(authority, `"https://rules.example/cn.srs"`, `"http://rules.example/cn.srs"`, 1),
		"unknown binding line": strings.Replace(authority, `{"rule_set_id":"cn","line_id":"direct"}`, `{"rule_set_id":"cn","line_id":"gone"}`, 1),
		"duplicate binding":    strings.Replace(authority, `{"rule_set_id":"cn","line_id":"direct"}`, `{"rule_set_id":"intranet","line_id":"direct"}`, 1),
		"no default exit":      strings.Replace(authority, `"name":"在国外","default_line_id":"direct",`, `"name":"在国外",`, 1),
		"unknown active":       strings.Replace(authority, `"active_scenario_id":"domestic"`, `"active_scenario_id":"gone"`, 1),
		"bad region":           strings.Replace(authority, `"cn":"domestic"`, `"china":"domestic"`, 1),
		"repeated region":      strings.Replace(authority, `"cn":"domestic"`, `"cn":"domestic","CN":"overseas"`, 1),
		"subscriptions":        strings.Replace(authority, `"active_scenario_id"`, `"subscriptions":[{"id":"s"}],"active_scenario_id"`, 1),
	}
	for name, seed := range cases {
		if seed == authority {
			t.Fatalf("%s: fixture did not change", name)
		}
		if _, err := Load([]byte(seed)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The allowlists mirror the phone's Swift model; a renamed key there must not
// leave the checker approving a field the phone no longer reads.
func TestMobileKeysExistInThePhoneModel(t *testing.T) {
	source, err := os.ReadFile("../../macos/Sources/XDial/Models.swift")
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []map[string]bool{mobileProfileKeys, mobileLineKeys, mobileScenarioKeys, mobileBindingKeys} {
		for key := range set {
			if strings.Contains(key, "_") && !strings.Contains(string(source), `"`+key+`"`) {
				t.Errorf("%s is not a key of the phone model", key)
			}
		}
	}
}

func TestRuleHistoryIsValidatedAndCompared(t *testing.T) {
	withHistory := strings.Replace(authority, `"revision":2,`,
		`"revision":2,"rule_set_history":{"intranet":[{"domains":["example.com","shop.example"],"cidrs":[]}]},`, 1)
	if withHistory == authority {
		t.Fatal("fixture did not change")
	}
	seed := mustLoad(t, withHistory)
	if len(seed.RuleHistory) != 1 {
		t.Fatalf("history not loaded: %v", seed.RuleHistory)
	}
	want := Fingerprint(seed, nil)
	reordered := strings.Replace(withHistory, `["example.com","shop.example"]`, `["SHOP.example","example.com"]`, 1)
	if d := Diff(want, Fingerprint(mustLoad(t, reordered), nil), nil); len(d) != 0 {
		t.Fatalf("order and case of earlier content reported as differences: %v", d)
	}
	if d := Diff(want, Fingerprint(mustLoad(t, authority), nil), nil); len(d) == 0 {
		t.Fatal("a package that drops the history must differ")
	}
	changed := strings.Replace(withHistory, `"shop.example"`, `"other.example"`, 1)
	if d := Diff(want, Fingerprint(mustLoad(t, changed), nil), nil); len(d) == 0 {
		t.Fatal("changed earlier content must differ")
	}
	for name, bad := range map[string]string{
		"url rule":    strings.Replace(withHistory, `"rule_set_history":{"intranet"`, `"rule_set_history":{"cn"`, 1),
		"unknown":     strings.Replace(withHistory, `"rule_set_history":{"intranet"`, `"rule_set_history":{"gone"`, 1),
		"extra field": strings.Replace(withHistory, `"cidrs":[]}]}`, `"cidrs":[],"invert":true}]}`, 1),
	} {
		if bad == withHistory {
			t.Fatalf("%s: fixture did not change", name)
		}
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRuleHistoryRejectsInvalidJSONShapes(t *testing.T) {
	for _, history := range []string{
		`null`, `[]`, `{"intranet":null}`, `{"intranet":{}}`,
		`{"intranet":[null]}`, `{"intranet":[[]]}`,
		`{"intranet":[{"domains":null}]}`, `{"intranet":[{"cidrs":null}]}`,
		`{"intranet":[{"domains":[null]}]}`, `{"intranet":[{"cidrs":[null]}]}`,
		`{"intranet":[{"domains":[1]}]}`, `{"intranet":[{"cidrs":"10.0.0.0/24"}]}`,
	} {
		t.Run(history, func(t *testing.T) {
			data := strings.Replace(authority, `"revision":2,`, `"revision":2,"rule_set_history":`+history+`,`, 1)
			if _, err := Load([]byte(data)); err == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
	for _, history := range []string{`{}`, `{"intranet":[]}`, `{"intranet":[{}]}`, `{"intranet":[{"domains":[],"cidrs":[]}]}`} {
		data := strings.Replace(authority, `"revision":2,`, `"revision":2,"rule_set_history":`+history+`,`, 1)
		mustLoad(t, data)
	}
}

const overseasHistory = `"scenario_history":{"overseas":[{"default_line_id":"direct","bindings":[{"rule_set_id":"intranet","line_id":"vpn"}]}]},`

func TestScenarioHistoryIsValidatedAndCompared(t *testing.T) {
	withHistory := strings.Replace(authority, `"revision":2,`, `"revision":2,`+overseasHistory, 1)
	seed := mustLoad(t, withHistory)
	if len(seed.ScenarioHistory) != 1 {
		t.Fatalf("history not loaded: %v", seed.ScenarioHistory)
	}
	// The history speaks in the imported IDs, like the rest of the seed.
	for id, versions := range seed.ScenarioHistory {
		if id != seed.Profile.Scenarios[1].ID || versions[0].DefaultLineID != seed.Profile.Lines[0].ID ||
			versions[0].Bindings[0] != (RoutingBinding{RuleSetID: seed.Profile.RuleSets[0].ID, LineID: seed.Profile.Lines[1].ID}) {
			t.Fatalf("history not in imported IDs: %s %+v", id, versions)
		}
	}
	want := Fingerprint(seed, nil)
	if d := Diff(want, Fingerprint(mustLoad(t, authority), nil), nil); len(d) == 0 {
		t.Fatal("a package that drops the history must differ")
	}
	if d := Diff(want, Fingerprint(mustLoad(t, authority), nil), []string{"scenario_history"}); len(d) != 0 {
		t.Fatalf("a declared capability limit must be tolerated: %v", d)
	}
	// Binding order decides routing, so it counts.
	reordered := strings.Replace(withHistory, `"bindings":[{"rule_set_id":"intranet","line_id":"vpn"}]}]}`,
		`"bindings":[{"rule_set_id":"cn","line_id":"vpn"},{"rule_set_id":"intranet","line_id":"vpn"}]}]}`, 1)
	other := strings.Replace(reordered, `{"rule_set_id":"cn","line_id":"vpn"},{"rule_set_id":"intranet","line_id":"vpn"}`,
		`{"rule_set_id":"intranet","line_id":"vpn"},{"rule_set_id":"cn","line_id":"vpn"}`, 1)
	if d := Diff(Fingerprint(mustLoad(t, reordered), nil), Fingerprint(mustLoad(t, other), nil), nil); len(d) == 0 {
		t.Fatal("binding order must count")
	}
}

func TestScenarioHistoryRejectsWhatTheAppsReject(t *testing.T) {
	for _, history := range []string{
		`null`, `[]`, `{"overseas":null}`, `{"overseas":{}}`, `{"overseas":[null]}`, `{"overseas":[[]]}`,
		`{"gone":[{"default_line_id":"direct"}]}`,
		`{"overseas":[{}]}`, `{"overseas":[{"default_line_id":""}]}`, `{"overseas":[{"default_line_id":"gone"}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":null}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":[null]}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":[{"rule_set_id":"intranet"}]}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":[{"rule_set_id":"gone","line_id":"vpn"}]}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":[{"rule_set_id":"intranet","line_id":"gone"}]}]}`,
		`{"overseas":[{"default_line_id":"direct","bindings":[{"rule_set_id":"intranet","line_id":"vpn","condition_ids":[]}]}]}`,
		`{"overseas":[{"default_line_id":"direct","name":"在国外"}]}`,
	} {
		t.Run(history, func(t *testing.T) {
			data := strings.Replace(authority, `"revision":2,`, `"revision":2,"scenario_history":`+history+`,`, 1)
			if _, err := Load([]byte(data)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	empty := strings.Replace(authority, `"revision":2,`, `"revision":2,"scenario_history":{"overseas":[{"default_line_id":"direct"}]},`, 1)
	if _, err := Load([]byte(empty)); err != nil {
		t.Fatalf("a version without bindings is valid: %v", err)
	}
}
