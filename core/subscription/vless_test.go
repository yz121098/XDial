package subscription

import (
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/kafeifei/xdial/core/config"
)

const (
	testSubVLESSUUID   = "b831381d-6324-4d53-ad4f-8cda48b30811"
	testSubRealityPBK  = "cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc"
	testSubRealitySID  = "0123456789abcdef"
)

func TestParseBase64VLESSRealityAndTLS(t *testing.T) {
	realityURI := "vless://" + testSubVLESSUUID + "@203.0.113.10:8443" +
		"?encryption=none&flow=xtls-rprx-vision&security=reality" +
		"&sni=www.microsoft.com&fp=chrome&pbk=" + testSubRealityPBK +
		"&sid=" + testSubRealitySID + "&spx=%2F238855bb94f7cf2&type=tcp&headerType=none&packetEncoding=xudp&tfo=1#gcp-us%20Reality"
	tlsURI := "vless://" + testSubVLESSUUID + "@[2001:db8::1]:8443?security=tls&alpn=h2,http/1.1#IPv6-VLESS"

	// Direct unencoded share URI must parse via Parse("", uri, "") and ParseShareURIs(uri)
	directLines, err := ParseShareURIs(realityURI)
	if err != nil || len(directLines) != 1 || directLines[0].Name != "gcp-us Reality" || directLines[0].VLESSPort != 8443 {
		t.Fatalf("ParseShareURIs failed on raw Xray/3x-ui VLESS Reality link: lines=%+v err=%v", directLines, err)
	}

	payload := base64.StdEncoding.EncodeToString([]byte(realityURI + "\n" + tlsURI))
	res, err := Parse("", payload, "base64")
	if err != nil {
		t.Fatalf("Parse VLESS base64: %v", err)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(res.Lines))
	}

	rLine := res.Lines[0]
	if rLine.Name != "gcp-us Reality" ||
		rLine.Type != config.LineTypeVLESS ||
		rLine.VLESSServer != "203.0.113.10" ||
		rLine.VLESSPort != 8443 ||
		rLine.VLESSUUID != testSubVLESSUUID ||
		rLine.VLESSFlow != "xtls-rprx-vision" ||
		rLine.VLESSSNI != "www.microsoft.com" ||
		rLine.VLESSClientFingerprint != "chrome" ||
		rLine.VLESSRealityPublicKey != testSubRealityPBK ||
		rLine.VLESSRealityShortID != testSubRealitySID ||
		rLine.VLESSPacketEncoding != "xudp" ||
		!rLine.UDP ||
		!rLine.TFO {
		t.Fatalf("unexpected Reality line: %+v", rLine)
	}

	tLine := res.Lines[1]
	if tLine.Name != "IPv6-VLESS" ||
		tLine.VLESSServer != "2001:db8::1" ||
		tLine.VLESSPort != 8443 ||
		tLine.VLESSSNI != "2001:db8::1" ||
		tLine.VLESSRealityPublicKey != "" ||
		!reflect.DeepEqual(tLine.VLESSALPN, []string{"h2", "http/1.1"}) {
		t.Fatalf("unexpected plain TLS VLESS line: %+v", tLine)
	}
}

func TestParseClashVLESSReality(t *testing.T) {
	yamlContent := `
proxies:
  - name: "Tokyo-Reality"
    type: vless
    server: 203.0.113.20
    port: 443
    uuid: ` + testSubVLESSUUID + `
    network: tcp
    tls: true
    udp: true
    flow: xtls-rprx-vision
    servername: www.microsoft.com
    client-fingerprint: safari
    packet-encoding: xudp
    reality-opts:
      public-key: ` + testSubRealityPBK + `
      short-id: ` + testSubRealitySID + `
proxy-groups:
  - name: "Proxy"
    type: select
    proxies:
      - "Tokyo-Reality"
rules:
  - MATCH,Proxy
`
	res, err := Parse("", yamlContent, "clash")
	if err != nil {
		t.Fatalf("Parse Clash VLESS: %v", err)
	}
	if len(res.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(res.Lines))
	}
	got := res.Lines[0]
	if got.Name != "Tokyo-Reality" ||
		got.Type != config.LineTypeVLESS ||
		got.VLESSServer != "203.0.113.20" ||
		got.VLESSPort != 443 ||
		got.VLESSUUID != testSubVLESSUUID ||
		got.VLESSFlow != "xtls-rprx-vision" ||
		got.VLESSSNI != "www.microsoft.com" ||
		got.VLESSClientFingerprint != "safari" ||
		got.VLESSRealityPublicKey != testSubRealityPBK ||
		got.VLESSRealityShortID != testSubRealitySID ||
		got.VLESSPacketEncoding != "xudp" ||
		!got.UDP {
		t.Fatalf("unexpected Clash VLESS line: %+v", got)
	}
}

func TestVLESSParsersRejectUnsupportedOrInvalidNodes(t *testing.T) {
	badURIs := []string{
		// Plain unencrypted VLESS must be rejected
		"vless://" + testSubVLESSUUID + "@203.0.113.10:443?security=none#Plain",
		// WebSocket transport not modeled in editable fields must fail closed
		"vless://" + testSubVLESSUUID + "@203.0.113.10:443?security=tls&type=ws#WS",
		// Reality missing SNI
		"vless://" + testSubVLESSUUID + "@203.0.113.10:443?security=reality&pbk=" + testSubRealityPBK + "#NoSNI",
		// Reality invalid public key
		"vless://" + testSubVLESSUUID + "@203.0.113.10:443?security=reality&sni=www.microsoft.com&pbk=bad#BadPBK",
		// Reality odd-length short_id
		"vless://" + testSubVLESSUUID + "@203.0.113.10:443?security=reality&sni=www.microsoft.com&pbk=" + testSubRealityPBK + "&sid=abc#BadSID",
	}
	for _, uri := range badURIs {
		if parsed, ok := parseVLESS(uri); ok {
			t.Fatalf("invalid VLESS URI %q was accepted: %+v", uri, parsed)
		}
	}

	badClash := []map[string]interface{}{
		{
			"name": "WS", "type": "vless", "server": "example.com", "port": 443,
			"uuid": testSubVLESSUUID, "tls": true, "network": "ws",
		},
		{
			"name": "NoTLS", "type": "vless", "server": "example.com", "port": 443,
			"uuid": testSubVLESSUUID, "tls": false,
		},
		{
			"name": "BadFlow", "type": "vless", "server": "example.com", "port": 443,
			"uuid": testSubVLESSUUID, "tls": true, "flow": "xtls-rprx-direct",
		},
	}
	for _, item := range badClash {
		if parsed, ok := clashProxyToLine(item); ok {
			t.Fatalf("invalid Clash VLESS %+v was accepted: %+v", item, parsed)
		}
	}
}
