package runtime

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/kafeifei/xdial/core/config"
	"github.com/kafeifei/xdial/windows/internal/store"
)

func stMarshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestManagerLifecycleAndLocalSOCKSConnection(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	port := freeLoopbackPort(t)
	prefs := st.LoadPreferences()
	prefs.IngressMode = store.IngressLocalSOCKS
	prefs.SOCKSPort = port
	if err := st.SavePreferences(prefs); err != nil {
		t.Fatalf("SavePreferences: %v", err)
	}

	mgr, err := NewManager(st, "v0.9.8-test", nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Shutdown()

	// Add a custom rule set and scenario
	if err := mgr.SaveRuleSet(config.RuleSet{
		Name:    "测试域名规则",
		Type:    config.RuleSetTypeManual,
		Enabled: true,
		Domains: []string{"example.com"},
	}); err != nil {
		t.Fatalf("SaveRuleSet: %v", err)
	}

	snap0 := mgr.GetSnapshot()
	if len(snap0.Editor.RuleSets) < 2 {
		t.Fatalf("expected >= 2 rules, got %d", len(snap0.Editor.RuleSets))
	}

	// Start real in-process Libbox + sing-box connection in local-socks mode
	if err := mgr.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := mgr.GetSnapshot()
		if s.Phase == PhaseConnected {
			break
		}
		if s.Phase == PhaseFailed {
			t.Fatalf("connection failed: %+v", s.Failure)
		}
		time.Sleep(80 * time.Millisecond)
	}

	snapConn := mgr.GetSnapshot()
	if snapConn.Phase != PhaseConnected {
		t.Fatalf("expected PhaseConnected, got %s", snapConn.Phase)
	}
	if !snapConn.IsProxyOnly {
		t.Fatalf("expected IsProxyOnly=true in local-socks mode")
	}

	// Verify 127.0.0.1:<port> mixed SOCKS5/HTTP listener is genuinely accepting TCP connections
	conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
	if dialErr != nil {
		t.Fatalf("expected mixed proxy listener on 127.0.0.1:%d, dial failed: %v", port, dialErr)
	}
	_ = conn.Close()

	// Changing port preference while connected must flag ConfigDirty = true
	prefs.SOCKSPort = freeLoopbackPort(t)
	if err := mgr.SavePreferences(prefs); err != nil {
		t.Fatalf("SavePreferences dirty check: %v", err)
	}
	if !mgr.GetSnapshot().ConfigDirty {
		t.Fatalf("expected ConfigDirty=true after changing port while connected")
	}

	if err := mgr.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := mgr.GetSnapshot().Phase; got != PhaseDisconnected {
		t.Fatalf("expected PhaseDisconnected, got %s", got)
	}

	// Verify iOS "xdial-mobile-configuration" exported envelope imports cleanly
	iosExport := `{
		"format": "xdial-mobile-configuration",
		"schema_version": 1,
		"contains_credentials": false,
		"contains_subscription_urls": false,
		"profile": {
			"lines": [
				{"id": "direct", "name": "直连", "type": "direct", "enabled": true},
				{"id": "corp-vpn", "name": "公司 VPN", "type": "vpn", "enabled": true, "vpn_server": "vpn.corp.example:8443", "vpn_username": "", "vpn_password": ""}
			],
			"rule_sets": [
				{"id": "intranet", "name": "公司内网", "type": "manual", "enabled": true, "domains": ["corp.example"], "cidrs": ["10.0.0.0/8"]},
				{"id": "cn-domains", "name": "国内域名", "type": "url", "enabled": false, "url": "", "format": "srs"}
			],
			"subscriptions": [],
			"scenarios": [
				{"id": "in-office", "name": "在公司", "match_ssids": ["Office"], "bindings": [], "default_line_id": "direct"},
				{"id": "in-cn", "name": "在国内", "bindings": [{"rule_set_id": "intranet", "line_id": "corp-vpn"}], "default_line_id": "direct"}
			],
			"active_scenario_id": "in-cn"
		}
	}`
	if err := mgr.ImportProfileContent("手机端导出", iosExport); err != nil {
		t.Fatalf("ImportProfileContent(iOS export) failed: %v", err)
	}
}

func TestFreshMobileImportReplacesBlankSeedAndSignInSwitchesScenario(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	port := freeLoopbackPort(t)
	prefs := st.LoadPreferences()
	prefs.IngressMode = store.IngressLocalSOCKS
	prefs.SOCKSPort = port
	_ = st.SavePreferences(prefs)

	mgr, err := NewManager(st, "v0.9.8-test", nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Shutdown()

	iosExport := `{
		"format": "xdial-mobile-configuration",
		"schema_version": 1,
		"contains_credentials": false,
		"contains_subscription_urls": false,
		"profile": {
			"lines": [
				{"id": "direct", "name": "直连", "type": "direct", "enabled": true},
				{"id": "corp-vpn", "name": "公司 VPN", "type": "vpn", "enabled": true, "vpn_server": "127.0.0.1:1", "vpn_username": "", "vpn_password": ""}
			],
			"rule_sets": [
				{"id": "xdial-connectivity-direct", "name": "连接验收 · Direct", "type": "manual", "enabled": true, "cidrs": ["1.0.0.1/32"]},
				{"id": "xdial-connectivity-anyconnect", "name": "连接验收 · 非 Direct 出口", "type": "manual", "enabled": true, "cidrs": ["1.1.1.1/32"]},
				{"id": "lan", "name": "本地局域网", "type": "manual", "enabled": true, "cidrs": ["192.168.0.0/16"]},
				{"id": "intranet", "name": "公司内网", "type": "manual", "enabled": true, "domains": ["corp.example"]},
				{"id": "cn-sites", "name": "国内网站", "type": "url", "enabled": false, "url": "", "format": "srs"},
				{"id": "cn-ips", "name": "国内 IP", "type": "url", "enabled": false, "url": "", "format": "srs"}
			],
			"subscriptions": [],
			"scenarios": [
				{
					"id": "office",
					"name": "在公司",
					"bindings": [{"rule_set_id": "xdial-connectivity-direct", "line_id": "direct"}],
					"default_line_id": "direct"
				},
				{
					"id": "mainland",
					"name": "中国内地",
					"bindings": [
						{"rule_set_id": "xdial-connectivity-direct", "line_id": "direct"},
						{"rule_set_id": "lan", "line_id": "direct"},
						{"rule_set_id": "cn-sites", "line_id": "direct"},
						{"rule_set_id": "cn-ips", "line_id": "direct"},
						{"rule_set_id": "intranet", "line_id": "corp-vpn"}
					],
					"default_line_id": "corp-vpn"
				},
				{
					"id": "abroad",
					"name": "境外",
					"bindings": [
						{"rule_set_id": "xdial-connectivity-direct", "line_id": "direct"},
						{"rule_set_id": "xdial-connectivity-anyconnect", "line_id": "corp-vpn"},
						{"rule_set_id": "intranet", "line_id": "corp-vpn"}
					],
					"default_line_id": "direct"
				}
			],
			"active_scenario_id": "office"
		}
	}`

	if err := mgr.ImportProfileContent("XDial-configuration", iosExport); err != nil {
		t.Fatalf("ImportProfileContent: %v", err)
	}

	snap := mgr.GetSnapshot()
	if len(snap.Editor.Sources) != 1 {
		t.Fatalf("expected blank seed to be replaced with 1 source, got %d", len(snap.Editor.Sources))
	}
	if len(snap.Editor.Scenarios) != 3 {
		t.Fatalf("expected 3 scenarios without leftover 日常直连, got %d", len(snap.Editor.Scenarios))
	}
	if snap.Editor.Scenarios[1].Name != "中国内地" {
		t.Fatalf("expected clean scenario name 中国内地 without prefix, got %q", snap.Editor.Scenarios[1].Name)
	}
	for _, rs := range snap.Editor.AllRuleSets {
		if isConnectivityAcceptanceRule(rs) {
			t.Fatalf("expected connectivity acceptance rule %q to be stripped", rs.Name)
		}
		if rs.Name == "国内网站" && (!rs.Enabled || rs.URL == "") {
			t.Fatalf("expected 国内网站 URL ruleset to be restored and enabled, got %+v", rs)
		}
	}

	// Connect Direct scenario "在公司" first, then verify SignInAndConnect while connected
	// does NOT fail with "请先断开当前连接再修改登录账号", and switches to "中国内地"!
	if err := mgr.Connect(); err != nil {
		t.Fatalf("Connect(在公司): %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.GetSnapshot().Phase == PhaseConnected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mgr.GetSnapshot().Phase != PhaseConnected {
		t.Fatalf("expected connected to 在公司, got %s", mgr.GetSnapshot().Phase)
	}

	if err := mgr.SignInAndConnect("corp-vpn", "127.0.0.1:1", "demo@corp.example", "demo-pass"); err != nil {
		t.Fatalf("SignInAndConnect while connected failed: %v", err)
	}

	afterSignIn := mgr.GetSnapshot()
	if afterSignIn.NeedsSignIn {
		t.Fatalf("expected NeedsSignIn=false after SignInAndConnect")
	}
	if afterSignIn.Editor.ActiveScenarioID != "mainland" {
		t.Fatalf("expected ActiveScenarioID to switch to mainland (中国内地), got %q", afterSignIn.Editor.ActiveScenarioID)
	}
}

func TestStartupRepairsLegacyMixedMobileLibraryWhilePreservingCredentials(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Simulate the legacy pre-fix state: default blank seed + second source "XDial-configuration"
	// with prefixed scenarios, connectivity rules, and saved VPN credentials.
	mgr1, err := NewManager(st, "v0.9.8-test", nil, nil)
	if err != nil {
		t.Fatalf("NewManager 1: %v", err)
	}
	rawLegacyImport := `{
		"lines": [
			{"id": "direct", "name": "直连", "type": "direct", "enabled": true},
			{"id": "corp-vpn", "name": "公司 VPN", "type": "vpn", "enabled": true, "vpn_server": "vpn.corp.example:8443", "vpn_username": "saved@corp.example", "vpn_password": "saved-secret-token"}
		],
		"rule_sets": [
			{"id": "xdial-connectivity-direct", "name": "连接验收 · Direct", "type": "manual", "enabled": true, "cidrs": ["1.0.0.1/32"]},
			{"id": "intranet", "name": "公司内网", "type": "manual", "enabled": true, "domains": ["corp.example"]}
		],
		"scenarios": [
			{"id": "office", "name": "在公司", "bindings": [{"rule_set_id": "xdial-connectivity-direct", "line_id": "direct"}], "default_line_id": "direct"},
			{"id": "mainland", "name": "中国内地", "bindings": [{"rule_set_id": "intranet", "line_id": "corp-vpn"}], "default_line_id": "corp-vpn"}
		],
		"active_scenario_id": "mainland"
	}`
	// Force two-source legacy state via PreviewProfileLibraryImport directly
	sourceID := "legacy-mobile-src"
	cloned, err := config.ImportProfileDocument([]byte(rawLegacyImport), sourceID)
	if err != nil {
		t.Fatalf("ImportProfileDocument: %v", err)
	}
	// Restore simulated saved credentials after ImportProfileDocument
	for i := range cloned.Lines {
		if cloned.Lines[i].Type == config.LineTypeVPN {
			cloned.Lines[i].VPNUsername = "saved@corp.example"
			cloned.Lines[i].VPNPassword = "saved-secret-token"
		}
	}
	encodedCloned, _ := stMarshalJSON(cloned)
	baseLib, _ := st.LoadLibrary()
	preview, err := config.PreviewProfileLibraryImport(baseLib, encodedCloned, "XDial-configuration", sourceID, false)
	if err != nil {
		t.Fatalf("PreviewProfileLibraryImport: %v", err)
	}
	committed, err := config.CommitProfileLibraryPreview(baseLib, preview)
	if err != nil {
		t.Fatalf("CommitProfileLibraryPreview: %v", err)
	}
	if err := st.SaveLibrary(committed); err != nil {
		t.Fatalf("SaveLibrary: %v", err)
	}
	mgr1.Shutdown()

	// Now boot a new Manager against that store -> repairImportedMobileLibraryLocked runs!
	mgr2, err := NewManager(st, "v0.9.8-test", nil, nil)
	if err != nil {
		t.Fatalf("NewManager 2: %v", err)
	}
	defer mgr2.Shutdown()

	snap := mgr2.GetSnapshot()
	if len(snap.Editor.Sources) != 1 {
		t.Fatalf("expected untouched default source to be removed, got %d sources", len(snap.Editor.Sources))
	}
	if len(snap.Editor.Scenarios) != 2 {
		t.Fatalf("expected 2 cleaned scenarios, got %d", len(snap.Editor.Scenarios))
	}
	if snap.Editor.Scenarios[0].Name != "在公司" || snap.Editor.Scenarios[1].Name != "中国内地" {
		t.Fatalf("expected stripped prefixes [在公司, 中国内地], got [%s, %s]", snap.Editor.Scenarios[0].Name, snap.Editor.Scenarios[1].Name)
	}
	activeSc := ""
	for _, sc := range snap.Editor.Scenarios {
		if sc.ID == snap.Editor.ActiveScenarioID {
			activeSc = sc.Name
		}
	}
	if activeSc != "中国内地" {
		t.Fatalf("expected ActiveScenario to auto-select 中国内地, got %q", activeSc)
	}
	if snap.NeedsSignIn {
		t.Fatalf("expected NeedsSignIn=false because DPAPI credentials must be preserved")
	}
	foundSavedCreds := false
	for _, l := range snap.Editor.AllLines {
		if l.Type == config.LineTypeVPN && l.VPNUsername == "saved@corp.example" && l.VPNPassword == "saved-secret-token" {
			foundSavedCreds = true
		}
	}
	if !foundSavedCreds {
		t.Fatalf("expected saved VPN credentials to remain intact after migration")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
