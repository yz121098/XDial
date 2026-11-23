package config

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

const (
	testVLESSUUID          = "b831381d-6324-4d53-ad4f-8cda48b30811"
	testRealityPublicKey32 = "cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc"
	testRealityShortID     = "0123456789abcdef"
)

func TestBuildVLESSRealityOutbound(t *testing.T) {
	line := Line{
		ID:                    "reality-jp",
		Name:                  "JP Reality",
		Type:                  LineTypeVLESS,
		Enabled:               true,
		VLESSServer:           "203.0.113.10",
		VLESSPort:             443,
		VLESSUUID:             testVLESSUUID,
		VLESSFlow:             "xtls-rprx-vision",
		VLESSSNI:              "www.microsoft.com",
		VLESSRealityPublicKey: testRealityPublicKey32,
		VLESSRealityShortID:   "0123456789ABCDEF",
		VLESSPacketEncoding:   "xudp",
		UDP:                   true,
		TFO:                   true,
	}

	outbound := buildProxyOutbound(&line)
	if outbound == nil {
		t.Fatal("expected valid VLESS + Reality outbound")
	}
	if outbound["type"] != "vless" ||
		outbound["tag"] != "proxy-reality-jp" ||
		outbound["server"] != "203.0.113.10" ||
		outbound["server_port"] != 443 ||
		outbound["uuid"] != testVLESSUUID ||
		outbound["flow"] != "xtls-rprx-vision" ||
		outbound["packet_encoding"] != "xudp" ||
		outbound["tcp_fast_open"] != true {
		t.Fatalf("unexpected VLESS outbound fields: %+v", outbound)
	}
	if _, hasUoT := outbound["udp_over_tcp"]; hasUoT {
		t.Fatalf("VLESS uses native XUDP/packetaddr and must not emit udp_over_tcp: %+v", outbound)
	}

	tls, ok := outbound["tls"].(map[string]interface{})
	if !ok || tls["enabled"] != true || tls["server_name"] != "www.microsoft.com" {
		t.Fatalf("unexpected VLESS TLS block: %+v", outbound["tls"])
	}
	utls, ok := tls["utls"].(map[string]interface{})
	if !ok || utls["enabled"] != true || utls["fingerprint"] != "chrome" {
		t.Fatalf("Reality client must enable uTLS (default chrome): %+v", tls["utls"])
	}
	reality, ok := tls["reality"].(map[string]interface{})
	if !ok ||
		reality["enabled"] != true ||
		reality["public_key"] != testRealityPublicKey32 ||
		reality["short_id"] != testRealityShortID {
		t.Fatalf("unexpected Reality block: %+v", tls["reality"])
	}

	// Standard padded base64 of the same 32-byte key must normalize to RawURLEncoding.
	rawBytes, err := base64.RawURLEncoding.DecodeString(testRealityPublicKey32)
	if err != nil {
		t.Fatal(err)
	}
	line.VLESSRealityPublicKey = base64.StdEncoding.EncodeToString(rawBytes)
	normalized := buildProxyOutbound(&line)
	if normalized == nil {
		t.Fatal("padded 32-byte base64 public key should be accepted and normalized")
	}
	normReality := normalized["tls"].(map[string]interface{})["reality"].(map[string]interface{})
	if normReality["public_key"] != testRealityPublicKey32 {
		t.Fatalf("public_key = %v, want %s", normReality["public_key"], testRealityPublicKey32)
	}
}

func TestBuildVLESSTLSOutboundWithoutReality(t *testing.T) {
	line := Line{
		ID:            "vless-tls",
		Type:          LineTypeVLESS,
		Enabled:       true,
		VLESSServer:   "vless.example.com",
		VLESSPort:     443,
		VLESSUUID:     testVLESSUUID,
		AllowInsecure: true,
		VLESSALPN:     []string{"h2", "http/1.1"},
	}
	outbound := buildProxyOutbound(&line)
	if outbound == nil {
		t.Fatal("expected valid plain-TLS VLESS outbound")
	}
	tls := outbound["tls"].(map[string]interface{})
	if tls["server_name"] != "vless.example.com" || tls["insecure"] != true {
		t.Fatalf("unexpected VLESS TLS options: %+v", tls)
	}
	if _, hasReality := tls["reality"]; hasReality {
		t.Fatalf("plain VLESS TLS must not emit reality block: %+v", tls)
	}
	if _, hasUTLS := tls["utls"]; hasUTLS {
		t.Fatalf("plain VLESS TLS without fingerprint must not force uTLS: %+v", tls)
	}
}

func TestBuildVLESSOutboundValidatesBoundaries(t *testing.T) {
	valid := Line{
		ID:                    "vless",
		Type:                  LineTypeVLESS,
		Enabled:               true,
		VLESSServer:           "203.0.113.10",
		VLESSPort:             443,
		VLESSUUID:             testVLESSUUID,
		VLESSFlow:             "xtls-rprx-vision",
		VLESSSNI:              "www.microsoft.com",
		VLESSRealityPublicKey: testRealityPublicKey32,
		VLESSRealityShortID:   "abcd",
	}

	cases := map[string]func(*Line){
		"bad uuid": func(l *Line) { l.VLESSUUID = "not-a-uuid" },
		"bad flow": func(l *Line) { l.VLESSFlow = "xtls-rprx-direct" },
		"bad packet encoding": func(l *Line) {
			l.VLESSPacketEncoding = "raw"
		},
		"bad fingerprint": func(l *Line) {
			l.VLESSClientFingerprint = "unknown-browser"
		},
		"short_id without public_key": func(l *Line) {
			l.VLESSRealityPublicKey = ""
			l.VLESSRealityShortID = "0123"
		},
		"reality without sni": func(l *Line) {
			l.VLESSSNI = ""
		},
		"reality with IP sni": func(l *Line) {
			l.VLESSSNI = "203.0.113.10"
		},
		"short public key": func(l *Line) {
			l.VLESSRealityPublicKey = "c2hvcnQ"
		},
		"odd-length short_id": func(l *Line) {
			l.VLESSRealityShortID = "abc"
		},
		"overlong short_id": func(l *Line) {
			l.VLESSRealityShortID = "0123456789abcdef00"
		},
		"non-hex short_id": func(l *Line) {
			l.VLESSRealityShortID = "zzzz"
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if ob := buildProxyOutbound(&candidate); ob != nil {
				t.Fatalf("expected invalid VLESS option (%s) to fail closed, got: %+v", name, ob)
			}
		})
	}
}

func TestVLESSDocumentRoundTripAndEndpointBootstrap(t *testing.T) {
	original := &Profile{
		ID: "p1",
		Lines: []Line{
			{ID: "direct", Name: "直连", Type: LineTypeDirect, Enabled: true},
			{
				ID:                     "reality",
				Name:                   "Reality",
				Type:                   LineTypeVLESS,
				Enabled:                true,
				VLESSServer:            "edge.example.net",
				VLESSPort:              443,
				VLESSUUID:              testVLESSUUID,
				VLESSFlow:              "xtls-rprx-vision",
				VLESSSNI:               "www.microsoft.com",
				VLESSClientFingerprint: "safari",
				VLESSRealityPublicKey:  testRealityPublicKey32,
				VLESSRealityShortID:    testRealityShortID,
				VLESSPacketEncoding:    "xudp",
			},
		},
		Scenarios:        []Scenario{{ID: "s1", Name: "Default", DefaultLineID: "reality"}},
		ActiveScenarioID: "s1",
	}

	exported, err := ExportProfileDocument(original)
	if err != nil {
		t.Fatalf("ExportProfileDocument: %v", err)
	}
	imported, err := ImportProfileDocument(exported, "doc")
	if err != nil {
		t.Fatalf("ImportProfileDocument: %v", err)
	}
	var got *Line
	for i := range imported.Lines {
		if imported.Lines[i].Type == LineTypeVLESS {
			got = &imported.Lines[i]
			break
		}
	}
	if got == nil {
		t.Fatal("imported profile missing VLESS line")
	}
	if got.VLESSServer != "edge.example.net" ||
		got.VLESSPort != 443 ||
		got.VLESSUUID != testVLESSUUID ||
		got.VLESSFlow != "xtls-rprx-vision" ||
		got.VLESSSNI != "www.microsoft.com" ||
		got.VLESSClientFingerprint != "safari" ||
		got.VLESSRealityPublicKey != testRealityPublicKey32 ||
		got.VLESSRealityShortID != testRealityShortID ||
		got.VLESSPacketEncoding != "xudp" {
		raw, _ := json.Marshal(got)
		t.Fatalf("round-trip mismatch: %s", raw)
	}

	rawTP, err := GenerateSingBoxTransparentProxy(
		original,
		29876,
		"session-user",
		"session-password",
		"/tmp",
		"utun-underlay",
		[]string{"192.0.2.53"},
	)
	if err != nil {
		t.Fatalf("GenerateSingBoxTransparentProxy: %v", err)
	}
	var cfg SingBoxConfig
	if err := json.Unmarshal(rawTP, &cfg); err != nil {
		t.Fatalf("unmarshal transparent proxy config: %v", err)
	}
	outbound := transparentProxyOutboundByTag(t, cfg, "proxy-reality")
	if outbound["domain_resolver"] != desktopPublicDNSTag {
		t.Fatalf("domain VLESS outbound in Transparent Proxy must attach bootstrap public-dns: %+v", outbound)
	}
}
