package cloudhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestVLESSRealityMaterialAndExistingRoundTrip(t *testing.T) {
	userUUID, privKey, pubKey, shortID, err := newVLESSRealityMaterial()
	if err != nil {
		t.Fatalf("newVLESSRealityMaterial: %v", err)
	}
	if userUUID == "" || privKey == "" || pubKey == "" || len(shortID) != 16 {
		t.Fatalf("unexpected generated material: uuid=%q priv=%q pub=%q sid=%q", userUUID, privKey, pubKey, shortID)
	}

	cfg, err := serverConfigVLESSRealityJSON(443, "www.microsoft.com", userUUID, privKey, shortID)
	if err != nil {
		t.Fatalf("serverConfigVLESSRealityJSON: %v", err)
	}

	existing := parseExisting(base64.StdEncoding.EncodeToString(cfg))
	if existing == nil {
		t.Fatalf("parseExisting returned nil for VLESS+Reality config: %s", cfg)
	}
	if existing.Protocol != ProtocolVLESSReality ||
		existing.Port != 443 ||
		existing.ServerName != "www.microsoft.com" ||
		existing.UUID != userUUID ||
		existing.Flow != vlessVisionFlow ||
		existing.PrivateKey != privKey ||
		existing.PublicKey != pubKey ||
		existing.ShortID != shortID {
		t.Fatalf("round-trip mismatch: got %+v", existing)
	}

	// Ensure Report JSON never leaks secrets.
	f := ubuntuFacts()
	f["xdial_config"] = base64.StdEncoding.EncodeToString(cfg)
	f["xdial_service"] = "active"
	report := evaluate(f, privilegeRoot)
	rawReport, _ := json.Marshal(report)
	for _, secret := range []string{userUUID, privKey, pubKey, shortID} {
		if strings.Contains(string(rawReport), secret) {
			t.Fatalf("Report leaked secret %q: %s", secret, rawReport)
		}
	}
}

func TestBuildPlanWithOptionsVLESSReality(t *testing.T) {
	f := ubuntuFacts()
	f["ufw"] = "Status: active"
	report := evaluate(f, privilegeRoot)

	// Port 443 is allowed for VLESS + Reality when free.
	plan, err := BuildPlanWithOptions(report, f, Options{
		Protocol:   ProtocolVLESSReality,
		Port:       443,
		ServerName: "www.apple.com",
	})
	if err != nil {
		t.Fatalf("BuildPlanWithOptions(443): %v", err)
	}
	if plan.Protocol != ProtocolVLESSReality || plan.Port != 443 || plan.ServerName != "www.apple.com" {
		t.Fatalf("unexpected plan header: %+v", plan)
	}
	foundTCPOnlyUFW := false
	for _, step := range plan.Steps {
		if step.ID == "ufw" && step.Detail == "ufw allow 443/tcp" {
			foundTCPOnlyUFW = true
		}
	}
	if !foundTCPOnlyUFW {
		t.Fatalf("VLESS + Reality ufw step should allow TCP only: %+v", plan.Steps)
	}

	// Port 443 must still be rejected if already occupied by e.g. nginx.
	occupied := report
	occupied.ListeningPorts = append(append([]int(nil), report.ListeningPorts...), 443)
	if _, err := BuildPlanWithOptions(occupied, f, Options{Protocol: ProtocolVLESSReality, Port: 443}); err == nil || AsError(err).Code != "port_in_use" {
		t.Fatalf("occupied port 443 must be rejected: %v", err)
	}

	// Privileged non-443 ports like 80 must still be rejected.
	if _, err := BuildPlanWithOptions(report, f, Options{Protocol: ProtocolVLESSReality, Port: 80}); err == nil || AsError(err).Code != "port_in_use" {
		t.Fatalf("port 80 must be rejected: %v", err)
	}

	// Invalid / IP camouflage server_name must be rejected.
	for _, badSNI := range []string{"203.0.113.10", "localhost", "bad host", "-bad.example.com"} {
		if _, err := BuildPlanWithOptions(report, f, Options{Protocol: ProtocolVLESSReality, Port: 443, ServerName: badSNI}); err == nil || AsError(err).Code != "invalid_server_name" {
			t.Fatalf("bad SNI %q should fail with invalid_server_name, got: %v", badSNI, err)
		}
	}
}

func TestDeployAndReuseVLESSRealityOverSSH(t *testing.T) {
	srv := newTestServer(t, "root", "test-pass")
	ctx := context.Background()

	var writtenConfig string
	srv.respond = func(cmd, input string) (string, int) {
		if strings.Contains(cmd, configPath+".xdial-tmp") && input != "" {
			writtenConfig = input
		}
		return "", 0
	}

	session := deploymentSession(t, srv)
	f := ubuntuFacts()
	f["singbox_path"] = defaultBinary
	session.facts = f
	r := evaluate(f, privilegeRoot)
	session.report = &r

	deployed, err := session.DeployWithOptions(ctx, Options{
		Protocol:   ProtocolVLESSReality,
		Port:       443,
		ServerName: "www.microsoft.com",
	}, nil)
	if err != nil {
		t.Fatalf("DeployWithOptions: %v", err)
	}
	if deployed.Protocol != ProtocolVLESSReality ||
		deployed.Port != 443 ||
		deployed.ServerName != "www.microsoft.com" ||
		deployed.Flow != "xtls-rprx-vision" ||
		deployed.Fingerprint != "chrome" ||
		deployed.UUID == "" ||
		deployed.PublicKey == "" ||
		len(deployed.ShortID) != 16 ||
		deployed.Reused {
		t.Fatalf("unexpected deploy result: %+v", deployed)
	}
	if !strings.Contains(writtenConfig, `"type": "vless"`) || !strings.Contains(writtenConfig, `"www.microsoft.com"`) {
		t.Fatalf("unexpected written server config: %s", writtenConfig)
	}

	// Re-evaluating with the written config and deploying with the same protocol/port/SNI reuses the existing proxy & keys.
	f2 := ubuntuFacts()
	f2["singbox_path"] = defaultBinary
	f2["xdial_config"] = base64.StdEncoding.EncodeToString([]byte(writtenConfig))
	f2["xdial_service"] = "active"
	r2 := evaluate(f2, privilegeRoot)
	if r2.Existing == nil || r2.Existing.Protocol != ProtocolVLESSReality || r2.Existing.Port != 443 {
		t.Fatalf("expected existing VLESS+Reality proxy, got: %+v", r2.Existing)
	}
	session.facts = f2
	session.report = &r2

	reused, err := session.DeployWithOptions(ctx, Options{
		Protocol:   ProtocolVLESSReality,
		Port:       443,
		ServerName: "www.microsoft.com",
	}, nil)
	if err != nil {
		t.Fatalf("reuse DeployWithOptions: %v", err)
	}
	if !reused.Reused ||
		reused.UUID != deployed.UUID ||
		reused.PublicKey != deployed.PublicKey ||
		reused.ShortID != deployed.ShortID {
		t.Fatalf("expected reused VLESS+Reality credentials to match: got %+v, want %+v", reused, deployed)
	}
}
