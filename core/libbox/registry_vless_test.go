package libbox

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kafeifei/xdial/core/config"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxservice "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/socks"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/things-go/go-socks5"
)

func TestOutboundRegistryIncludesVLESSAndRedactsSecrets(t *testing.T) {
	options, ok := outboundRegistry().CreateOptions("vless")
	if !ok {
		t.Fatal("VLESS outbound is not registered in runtime registry")
	}
	if _, ok := options.(*option.VLESSOutboundOptions); !ok {
		t.Fatalf("unexpected VLESS options type: %T", options)
	}

	line := config.Line{
		ID:                     "reality",
		Name:                   "Reality",
		Type:                   config.LineTypeVLESS,
		Enabled:                true,
		VLESSServer:            "203.0.113.10",
		VLESSPort:              443,
		VLESSUUID:              "b831381d-6324-4d53-ad4f-8cda48b30811",
		VLESSFlow:              "xtls-rprx-vision",
		VLESSSNI:               "www.microsoft.com",
		VLESSClientFingerprint: "chrome",
		VLESSRealityPublicKey:  "cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc",
		VLESSRealityShortID:    "0123456789abcdef",
	}
	if !subscriptionNodeUsable(line) {
		t.Fatal("valid VLESS + Reality subscription node must be usable")
	}
	line.VLESSRealityPublicKey = "invalid"
	if subscriptionNodeUsable(line) {
		t.Fatal("invalid VLESS Reality public key must make subscription node unusable")
	}

	line.VLESSRealityPublicKey = "cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc"
	profile := &config.Profile{
		ID:               "p1",
		Lines:            []config.Line{{ID: "direct", Name: "Direct", Type: config.LineTypeDirect, Enabled: true}, line},
		Scenarios:        []config.Scenario{{ID: "s1", Name: "Default", DefaultLineID: "reality"}},
		ActiveScenarioID: "s1",
	}
	rawProfile, _ := json.Marshal(profile)
	exported, err := ExportProfile(string(rawProfile))
	if err != nil {
		t.Fatalf("ExportProfile: %v", err)
	}
	if strings.Contains(exported, line.VLESSUUID) {
		t.Fatalf("ExportProfile leaked VLESS UUID: %s", exported)
	}

	shareURI := "vless://00000000-0000-4000-8000-000000000001@203.0.113.199:8443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.microsoft.com&fp=chrome&pbk=cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc&sid=0123456789abcdef&spx=%2F238855bb94f7cf2&type=tcp&headerType=none#gcp-us%20Reality"
	linksJSON, err := ParseShareLinks(shareURI)
	if err != nil || !strings.Contains(linksJSON, `"gcp-us Reality"`) {
		t.Fatalf("ParseShareLinks failed: json=%s err=%v", linksJSON, err)
	}
	subJSON, err := ParseSubscription(shareURI, "", "auto")
	if err != nil || !strings.Contains(subJSON, `"gcp-us Reality"`) {
		t.Fatalf("ParseSubscription inline share URI failed: json=%s err=%v", subJSON, err)
	}
}

func TestVLESSRealityLoopbackHandshakeAndTraffic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		curves []tls.CurveID
		chain  bool
	}{
		{name: "post-quantum-x25519mlkem768", curves: []tls.CurveID{tls.X25519MLKEM768, tls.X25519}},
		{name: "classic-x25519-only", curves: []tls.CurveID{tls.X25519}},
		{name: "proxy-chain", curves: []tls.CurveID{tls.X25519}, chain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runVLESSRealityLoopbackHandshakeAndTraffic(t, tc.curves, tc.chain)
		})
	}
}

func runVLESSRealityLoopbackHandshakeAndTraffic(t *testing.T, curves []tls.CurveID, chain bool) {
	// 1. Local TLS 1.3 camouflage target for Reality server handshake borrowing.
	camouflage := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	camouflage.TLS = &tls.Config{
		MinVersion:       tls.VersionTLS13,
		MaxVersion:       tls.VersionTLS13,
		CurvePreferences: curves,
	}
	camouflage.StartTLS()
	defer camouflage.Close()
	_, camoPortStr, _ := net.SplitHostPort(camouflage.Listener.Addr().String())
	camoPort, _ := strconv.Atoi(camoPortStr)

	// 2. Target HTTP server reached through VLESS + Reality proxy.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless-reality-ok"))
	}))
	defer target.Close()

	// 3. Generate X25519 Reality keypair, UUID, and short_id.
	userUUID := uuid.NewString()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sidBytes := make([]byte, 8)
	if _, err := rand.Read(sidBytes); err != nil {
		t.Fatal(err)
	}
	privB64 := base64.RawURLEncoding.EncodeToString(priv.Bytes())
	pubB64 := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	shortID := hex.EncodeToString(sidBytes)

	// Pick free TCP port for VLESS + Reality server inbound.
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	vlessPort := probeLn.Addr().(*net.TCPAddr).Port
	_ = probeLn.Close()

	// 4. Start sing-box VLESS + Reality server instance.
	serverInReg := inbound.NewRegistry()
	vless.RegisterInbound(serverInReg)
	serverOutReg := outbound.NewRegistry()
	direct.RegisterOutbound(serverOutReg)
	serverCtx := box.Context(
		context.Background(),
		serverInReg,
		serverOutReg,
		endpoint.NewRegistry(),
		dnsTransportRegistry(),
		boxservice.NewRegistry(),
		certificate.NewRegistry(),
	)
	serverConfig := fmt.Sprintf(`{
		"log": {"disabled": true},
		"inbounds": [{
			"type": "vless",
			"tag": "xdial-vless",
			"listen": "127.0.0.1",
			"listen_port": %d,
			"users": [{"name": "xdial", "uuid": %q, "flow": "xtls-rprx-vision"}],
			"tls": {
				"enabled": true,
				"server_name": "www.microsoft.com",
				"reality": {
					"enabled": true,
					"handshake": {"server": "127.0.0.1", "server_port": %d},
					"private_key": %q,
					"short_id": [%q]
				}
			}
		}],
		"outbounds": [{"type": "direct", "tag": "direct"}]
	}`, vlessPort, userUUID, camoPort, privB64, shortID)

	serverOpts, err := json.UnmarshalExtendedContext[option.Options](serverCtx, []byte(serverConfig))
	if err != nil {
		t.Fatalf("unmarshal server options: %v", err)
	}
	serverBox, err := box.New(box.Options{Context: serverCtx, Options: serverOpts})
	if err != nil {
		t.Fatalf("create server box: %v", err)
	}
	if err := serverBox.Start(); err != nil {
		_ = serverBox.Close()
		t.Fatalf("start server box: %v", err)
	}
	defer serverBox.Close()

	// 5. Build client sing-box instance from core/config.LineOutbound + core/libbox runtime registry.
	line := config.Line{
		ID:                     "loopback-reality",
		Name:                   "Loopback Reality",
		Type:                   config.LineTypeVLESS,
		Enabled:                true,
		VLESSServer:            "127.0.0.1",
		VLESSPort:              vlessPort,
		VLESSUUID:              userUUID,
		VLESSFlow:              "xtls-rprx-vision",
		VLESSSNI:               "www.microsoft.com",
		VLESSClientFingerprint: "chrome",
		VLESSRealityPublicKey:  pubB64,
		VLESSRealityShortID:    shortID,
		UDP:                    true,
	}
	clientCtx := boxContext(context.Background())
	profile := &config.Profile{Lines: []config.Line{line}, Scenarios: []config.Scenario{{ID: "test", DefaultLineID: line.ID}}, ActiveScenarioID: "test"}
	var unavailableCtx context.Context
	var disconnectedDoc []byte
	var bridgePort int
	if chain {
		profile.Lines = append(profile.Lines, config.Line{ID: "entry", Type: config.LineTypeVPN, Enabled: true}, config.Line{ID: "chain", Type: config.LineTypeChain, Enabled: true, GroupMembers: []string{"entry", line.ID}})
		profile.Scenarios[0].DefaultLineID = "chain"
		// The real Windows/in-process VPN outbound must not bypass an offline
		// bridge even though the Reality server is directly reachable here.
		disconnectedDoc, err = config.GenerateSingBoxFor(profile, 0, "", config.PlatformNE, "")
		if err != nil {
			t.Fatal(err)
		}
		unavailableCtx = service.ContextWith(boxContext(context.Background()), &anyConnectLineRuntime{})

		// Loopback stand-in for the desktop VPN SOCKS bridge. Only it can map
		// the test endpoint to Reality. It must never receive the HTTP target.
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		bridgePort = ln.Addr().(*net.TCPAddr).Port
		profile.Lines[0].VLESSServer = "192.0.2.33"
		server := socks5.NewServer(socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != net.JoinHostPort("192.0.2.33", strconv.Itoa(vlessPort)) {
				return nil, fmt.Errorf("unexpected bridge destination: %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(vlessPort)))
		}))
		go server.Serve(ln)
		socks.RegisterOutbound(service.FromContext[adapter.OutboundRegistry](clientCtx).(*outbound.Registry))
	}
	// Only take the generated outbounds/final: no system ingress in the test.
	data, err := config.GenerateSingBox(profile, bridgePort, "")
	if err != nil {
		t.Fatal(err)
	}
	var generated config.SingBoxConfig
	if err = json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	final := generated.Route["final"].(string)
	clientDoc, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": generated.Outbounds, "route": map[string]any{"final": final}})
	if err != nil {
		t.Fatal(err)
	}
	clientOpts, err := json.UnmarshalExtendedContext[option.Options](clientCtx, clientDoc)
	if err != nil {
		t.Fatalf("unmarshal client options: %v", err)
	}
	clientBox, err := box.New(box.Options{Context: clientCtx, Options: clientOpts})
	if err != nil {
		t.Fatalf("create client box: %v", err)
	}
	if err := clientBox.Start(); err != nil {
		_ = clientBox.Close()
		t.Fatalf("start client box: %v", err)
	}
	defer clientBox.Close()

	proxyOutbound, loaded := clientBox.Outbound().Outbound(final)
	if !loaded {
		t.Fatalf("outbound %q not loaded", final)
	}

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return proxyOutbound.DialContext(ctx, network, M.ParseSocksaddr(addr))
			},
		},
	}

	resp, err := httpClient.Get(target.URL)
	if err != nil {
		t.Fatalf("HTTP GET through VLESS + Reality failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != "vless-reality-ok" {
		t.Fatalf("body = %q, want %q", string(body), "vless-reality-ok")
	}
	if chain {
		opts, err := json.UnmarshalExtendedContext[option.Options](unavailableCtx, disconnectedDoc)
		if err != nil {
			t.Fatal(err)
		}
		opts.Inbounds = nil
		opts.Experimental = nil
		instance, err := box.New(box.Options{Context: unavailableCtx, Options: opts})
		if err != nil {
			t.Fatal(err)
		}
		defer instance.Close()
		if err = instance.Start(); err != nil {
			t.Fatal(err)
		}
		exit, ok := instance.Outbound().Outbound(final)
		if !ok {
			t.Fatal("missing chain")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := exit.DialContext(ctx, "tcp", M.ParseSocksaddr(target.Listener.Addr().String()))
		if conn != nil {
			conn.Close()
		}
		if !errors.Is(err, errAnyConnectLineUnavailable) {
			t.Fatalf("offline AnyConnect must fail closed: %v", err)
		}
	}
}
