package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func chainFixture() *Profile {
	p := testProfile()
	p.ID = "home-profile"
	p.Lines = append(p.Lines,
		Line{ID: "reality", Type: LineTypeVLESS, Enabled: true, VLESSServer: "exit.internal", VLESSPort: 443, VLESSUUID: testVLESSUUID, VLESSSNI: "sni.example", VLESSFlow: "xtls-rprx-vision", VLESSRealityPublicKey: testRealityPublicKey32},
		Line{ID: "home", Type: LineTypeChain, Enabled: true, GroupMembers: []string{"vpn", "reality"}})
	p.ActiveScenario().DefaultLineID = "home"
	p.ActiveScenario().Bindings = nil
	return p
}

func TestProxyChainEntryExitIsolation(t *testing.T) {
	p := chainFixture()
	// The same server remains independently selectable outside the chain.
	p.ActiveScenario().Bindings = []RuleBinding{{RuleSetID: "internal", LineID: "reality"}}
	for _, platform := range []Platform{PlatformMacOS, PlatformNE, PlatformTransparentProxy} {
		var cfg SingBoxConfig
		if platform == PlatformTransparentProxy {
			cfg, _ = generateTransparentProxyDNSTestConfig(t, p)
		} else {
			var raw []byte
			var err error
			if platform == PlatformMacOS {
				raw, err = GenerateSingBoxDesktop(p, 1234, "", t.TempDir(), []string{"192.0.2.53"}, "eth0")
			} else {
				raw, err = GenerateSingBoxFor(p, 1234, "", platform, t.TempDir(), "eth0")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
		}
		exit := transparentProxyOutboundByTag(t, cfg, "proxy-home")
		if cfg.Route["final"] != "proxy-home" || exit["type"] != "vless" || exit["detour"] != "vpn" || exit["server"] != "exit.internal" || exit["tls"].(map[string]any)["reality"] == nil {
			t.Fatal("incorrect chain exit or bootstrap", exit, cfg.Route)
		}
		if platform == PlatformMacOS {
			r := exit["domain_resolver"].(map[string]any)
			if transparentProxyDNSServerByTag(t, cfg, r["server"].(string))["detour"] != "vpn" {
				t.Fatal("bootstrap must use tunnel DNS")
			}
		} else if exit["domain_resolver"] != nil {
			t.Fatal("in-process VPN must receive endpoint name")
		}
		if transparentProxyOutboundByTag(t, cfg, "proxy-reality")["detour"] != nil {
			t.Fatal("mutated shared exit")
		}
		vpn := transparentProxyOutboundByTag(t, cfg, "vpn")
		if platform.isNetworkExtension() && vpn["type"] != "vpn" {
			t.Fatal(vpn)
		}
		resolver := cfg.DNS["final"].(string)
		if transparentProxyDNSServerByTag(t, cfg, resolver)["detour"] != "proxy-home" {
			t.Fatal("DNS did not use exit", cfg.DNS)
		}
	}
	plan, err := BuildConnectionPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range plan.Tasks {
		if task.ID == "line:home" {
			found = reflect.DeepEqual(task.Dependencies, []string{"line:vpn"})
		}
	}
	if !found || p.ActiveVPNLine() == nil || p.ActiveVPNLine().ID != "vpn" {
		t.Fatal(plan)
	}
	p.FindLine("vpn").VPNPassword = "changed"
	next, err := BuildConnectionPlan(p)
	if err != nil || plan.ConfigurationFingerprint == next.ConfigurationFingerprint {
		t.Fatalf("entry not fingerprinted: %v", err)
	}
	p.ActiveScenario().Bindings = nil
	p.ActiveScenario().DefaultLineID = "reality"
	raw, err := GenerateSingBoxFor(p, 0, "", PlatformNE, "")
	if err != nil || p.ActiveVPNLine() != nil || strings.Contains(string(raw), `"tag": "vpn"`) || strings.Contains(string(raw), "proxy-home") {
		t.Fatalf("unused chain activated: %v", err)
	}
}

func TestProxyChainIntermediateAndRuleBinding(t *testing.T) {
	p := chainFixture()
	p.ActiveScenario().DefaultLineID = "direct"
	p.ActiveScenario().Bindings = []RuleBinding{{RuleSetID: "internal", LineID: "home"}}
	p.FindLine("home").GroupMembers = []string{"vpn", "ss", "reality"}
	cfg, _ := generateTransparentProxyDNSTestConfig(t, p)
	exit := transparentProxyOutboundByTag(t, cfg, "proxy-home")
	middle := transparentProxyOutboundByTag(t, cfg, exit["detour"].(string))
	if middle["type"] != "trojan" || middle["detour"] != "vpn" || middle["domain_resolver"] != nil || cfg.Route["final"] != "direct" {
		t.Fatal(cfg)
	}
	for _, ob := range cfg.Outbounds {
		if ob["tag"] == "proxy-ss" || ob["tag"] == "proxy-reality" {
			t.Fatal("unused standalone hop", ob)
		}
	}
	p.RuleSets[0].Enabled = false
	if p.ActiveVPNLine() != nil {
		t.Fatal("disabled binding started chain")
	}
}

func TestProxyChainRejectsInvalidGraph(t *testing.T) {
	for _, members := range [][]string{{"vpn"}, {"reality", "vpn"}, {"reality", "reality"}, {"vpn", "missing"}, {"vpn", "home"}, {"direct", "reality"}} {
		p := chainFixture()
		p.FindLine("home").GroupMembers = members
		if _, err := BuildConnectionPlan(p); err == nil {
			t.Fatalf("plan accepted %v", members)
		}
		if _, err := GenerateSingBoxFor(p, 0, "", PlatformNE, ""); err == nil {
			t.Fatalf("compiled %v", members)
		}
	}
	p := chainFixture()
	p.FindLine("vpn").Enabled = false
	if _, err := BuildConnectionPlan(p); err == nil {
		t.Fatal("disabled entry accepted")
	}
	p = chainFixture()
	p.FindLine("reality").VLESSUUID = ""
	if _, err := BuildConnectionPlan(p); err == nil {
		t.Fatal("incomplete exit accepted")
	}
	p = chainFixture()
	p.Lines = append(p.Lines, Line{ID: "second", Type: LineTypeVPN, Enabled: true})
	p.ActiveScenario().Bindings = []RuleBinding{{RuleSetID: "internal", LineID: "second"}}
	if _, err := BuildConnectionPlan(p); err == nil {
		t.Fatal("two distinct VPN identities accepted")
	}
}

func TestProxyChainProfileRoundTrip(t *testing.T) {
	p := chainFixture()
	doc, err := ExportProfileDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportProfileDocument(doc, "copy")
	if err != nil {
		t.Fatal(err)
	}
	chain := imported.FindLine(imported.ActiveScenario().DefaultLineID)
	if chain.ID == "home" || imported.FindLine(chain.GroupMembers[0]).Type != LineTypeVPN || imported.FindLine(chain.GroupMembers[1]).Type != LineTypeVLESS {
		t.Fatal(chain)
	}
	raw, err := json.Marshal(imported)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := NewProfileLibrary(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	projection, err := ProfileLibraryRuntime(lib)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := ParseRuntimeProfile([]byte(projection))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rp.FindLine(chain.ID).GroupMembers, chain.GroupMembers) {
		t.Fatal("chain order lost")
	}
}
