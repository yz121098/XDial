package libbox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kafeifei/xdial/core/cloudhost"
	"github.com/kafeifei/xdial/core/config"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/option"
	jsonext "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/ntp"
)

// CloudHostListener receives setup progress as JSON; events carry no secrets.
type CloudHostListener interface {
	OnEvent(eventJSON string)
}

// CloudHostSetup drives one "add my server" session from the app. Every method
// returns {"ok":true,"data":...} or {"ok":false,"error":{code,message,message_en}}
// so the app can show the reason in either language. Credentials stay in memory
// for the life of this object and are never logged.
type CloudHostSetup struct {
	mu      sync.Mutex
	session *cloudhost.Session
	cancel  context.CancelFunc
}

func NewCloudHostSetup() *CloudHostSetup { return &CloudHostSetup{} }

func cloudHostReply(data interface{}, err error) string {
	var raw []byte
	if err != nil {
		raw, _ = json.Marshal(map[string]interface{}{"ok": false, "error": cloudhost.AsError(err)})
	} else {
		raw, _ = json.Marshal(map[string]interface{}{"ok": true, "data": data})
	}
	return string(raw)
}

func (c *CloudHostSetup) begin(timeout time.Duration) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	c.cancel = cancel
	return ctx
}

func (c *CloudHostSetup) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func parseCloudHostTarget(targetJSON string) (cloudhost.Target, error) {
	var target cloudhost.Target
	if len(targetJSON) > 64*1024 || json.Unmarshal([]byte(targetJSON), &target) != nil {
		return target, fmt.Errorf("invalid request")
	}
	return target, nil
}

// ScanHostKey returns the server's host key without sending any credential.
func (c *CloudHostSetup) ScanHostKey(targetJSON string) string {
	target, err := parseCloudHostTarget(targetJSON)
	if err != nil {
		return cloudHostReply(nil, err)
	}
	ctx := c.begin(30 * time.Second)
	defer c.end()
	key, err := cloudhost.ScanHostKey(ctx, target)
	return cloudHostReply(key, err)
}

// ConnectAndInspect signs in with the confirmed fingerprint and inspects the
// server read-only. Nothing on the server changes.
func (c *CloudHostSetup) ConnectAndInspect(targetJSON string) string {
	target, err := parseCloudHostTarget(targetJSON)
	if err != nil {
		return cloudHostReply(nil, err)
	}
	ctx := c.begin(90 * time.Second)
	defer c.end()
	c.closeSession()
	session, err := cloudhost.Connect(ctx, target)
	if err != nil {
		return cloudHostReply(nil, err)
	}
	report, err := session.Inspect(ctx)
	if err != nil {
		session.Close()
		return cloudHostReply(nil, err)
	}
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	return cloudHostReply(report, nil)
}

func parseCloudHostOptions(optionsJSON string) (cloudhost.Options, error) {
	var opts cloudhost.Options
	if len(optionsJSON) > 8*1024 || json.Unmarshal([]byte(optionsJSON), &opts) != nil {
		return opts, fmt.Errorf("invalid request")
	}
	return opts, nil
}

// Plan lists the exact changes for the port (0 picks the suggested one).
func (c *CloudHostSetup) Plan(port int) string {
	session := c.current()
	if session == nil {
		return cloudHostReply(nil, fmt.Errorf("no session"))
	}
	plan, err := session.Plan(port)
	return cloudHostReply(plan, err)
}

// PlanWithOptions lists the exact changes for the chosen protocol, port and Reality SNI.
func (c *CloudHostSetup) PlanWithOptions(optionsJSON string) string {
	opts, err := parseCloudHostOptions(optionsJSON)
	if err != nil {
		return cloudHostReply(nil, err)
	}
	session := c.current()
	if session == nil {
		return cloudHostReply(nil, fmt.Errorf("no session"))
	}
	plan, err := session.PlanWithOptions(opts)
	return cloudHostReply(plan, err)
}

// Deploy applies the plan the user confirmed. The returned data includes the
// proxy key, which the app stores with its existing credential handling.
func (c *CloudHostSetup) Deploy(port int, listener CloudHostListener) string {
	session := c.current()
	if session == nil {
		return cloudHostReply(nil, fmt.Errorf("no session"))
	}
	ctx := c.begin(15 * time.Minute)
	defer c.end()
	result, err := session.Deploy(ctx, port, func(event cloudhost.Event) {
		if listener == nil {
			return
		}
		if raw, err := json.Marshal(event); err == nil {
			listener.OnEvent(string(raw))
		}
	})
	return cloudHostReply(result, err)
}

// DeployWithOptions applies the confirmed plan for the chosen protocol, port and Reality SNI.
func (c *CloudHostSetup) DeployWithOptions(optionsJSON string, listener CloudHostListener) string {
	opts, err := parseCloudHostOptions(optionsJSON)
	if err != nil {
		return cloudHostReply(nil, err)
	}
	session := c.current()
	if session == nil {
		return cloudHostReply(nil, fmt.Errorf("no session"))
	}
	ctx := c.begin(15 * time.Minute)
	defer c.end()
	result, err := session.DeployWithOptions(ctx, opts, func(event cloudhost.Event) {
		if listener == nil {
			return
		}
		if raw, err := json.Marshal(event); err == nil {
			listener.OnEvent(string(raw))
		}
	})
	return cloudHostReply(result, err)
}

// Cancel stops the running step; Deploy then rolls back what it did.
func (c *CloudHostSetup) Cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

// Close ends the SSH session and forgets the credentials.
func (c *CloudHostSetup) Close() {
	c.Cancel()
	c.closeSession()
}

func (c *CloudHostSetup) current() *cloudhost.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func (c *CloudHostSetup) closeSession() {
	c.mu.Lock()
	session := c.session
	c.session = nil
	c.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

const (
	throughputURL   = "https://speed.cloudflare.com/__down?bytes=10000000"
	exitTraceURL    = "https://1.1.1.1/cdn-cgi/trace"
	throughputLimit = 12 * time.Second
)

type lineThroughput struct {
	LatencyMS     int     `json:"latency_ms"`
	ExitIP        string  `json:"exit_ip,omitempty"`
	DownloadBytes int64   `json:"download_bytes"`
	DownloadMS    int64   `json:"download_ms"`
	Mbps          float64 `json:"mbps"`
	ObservedAt    int64   `json:"observed_at"`
}

// ProbeStandaloneLineThroughput checks a line end to end without touching the
// running connection: a temporary session with only this proxy makes real
// HTTPS requests through it, measuring latency, the exit address and download
// speed. It succeeds only if an HTTPS response actually came back through the proxy.
func ProbeStandaloneLineThroughput(lineJSON string, timeoutMS int) string {
	var line config.Line
	if len(lineJSON) > 1024*1024 || json.Unmarshal([]byte(lineJSON), &line) != nil {
		return cloudHostReply(nil, fmt.Errorf("invalid line"))
	}
	result, err := probeLineThroughput(&line, timeoutMS)
	return cloudHostReply(result, err)
}

func probeLineThroughput(line *config.Line, timeoutMS int) (*lineThroughput, error) {
	if timeoutMS < 5000 {
		timeoutMS = 5000
	}
	if timeoutMS > 60000 {
		timeoutMS = 60000
	}
	document, err := standaloneLineProbeOptions(line)
	if err != nil {
		return nil, cloudHostProbeError("probe_unsupported", err.Error(), "This line cannot be tested on its own")
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	ctx := boxContext(parent)
	raw, _ := json.Marshal(document)
	options, err := jsonext.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		return nil, cloudHostProbeError("probe_config", "线路配置无法用于测试", "The line configuration cannot be tested")
	}
	instance, err := box.New(box.Options{Options: options, Context: ctx})
	if err != nil {
		return nil, cloudHostProbeError("probe_start", "无法创建测试会话", "Could not create the test session")
	}
	defer instance.Close()
	if err = instance.Start(); err != nil {
		return nil, cloudHostProbeError("probe_start", "无法启动测试会话", "Could not start the test session")
	}
	outbound, exists := instance.Outbound().Outbound("measurement")
	if !exists {
		return nil, cloudHostProbeError("probe_start", "测试线路不可用", "The line under test is unavailable")
	}

	latencyCtx, latencyCancel := context.WithTimeout(ctx, 10*time.Second)
	delay, err := urltest.URLTest(latencyCtx, lineLatencyTestURL, outbound)
	latencyCancel()
	if err != nil {
		return nil, cloudHostProbeError("proxy_unreachable",
			"经这条线路的 HTTPS 请求没有成功。请确认服务商的安全组或防火墙已放行代理端口（TCP 和 UDP），然后重新测试。",
			"An HTTPS request through this line did not succeed. Make sure your provider's security group or firewall allows the proxy port (TCP and UDP), then test again.")
	}
	result := &lineThroughput{LatencyMS: int(delay), ObservedAt: time.Now().UnixMilli()}

	client := proxiedHTTPClient(ctx, outbound)
	defer client.CloseIdleConnections()
	if body, err := proxiedGet(ctx, client, exitTraceURL, 64*1024, 10*time.Second); err == nil {
		for _, field := range strings.Split(string(body), "\n") {
			if value, ok := strings.CutPrefix(field, "ip="); ok {
				result.ExitIP = strings.TrimSpace(value)
			}
		}
	}

	downloadCtx, downloadCancel := context.WithTimeout(ctx, throughputLimit)
	defer downloadCancel()
	request, _ := http.NewRequestWithContext(downloadCtx, http.MethodGet, throughputURL, nil)
	request.Header.Set("Cache-Control", "no-cache")
	started := time.Now()
	response, err := client.Do(request)
	if err == nil {
		if response.StatusCode == http.StatusOK {
			result.DownloadBytes, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<20))
		}
		response.Body.Close()
	}
	elapsed := time.Since(started)
	result.DownloadMS = elapsed.Milliseconds()
	if result.DownloadBytes > 0 && elapsed > 0 {
		result.Mbps = float64(result.DownloadBytes*8) / elapsed.Seconds() / 1e6
	}
	return result, nil
}

func proxiedHTTPClient(ctx context.Context, outbound adapter.Outbound) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
				return outbound.DialContext(dialCtx, "tcp", M.ParseSocksaddr(address))
			},
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				Time:       ntp.TimeFuncFromContext(ctx),
				RootCAs:    adapter.RootPoolFromContext(ctx),
			},
			ForceAttemptHTTP2: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func proxiedGet(ctx context.Context, client *http.Client, url string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

func cloudHostProbeError(code, message, english string) error {
	return &cloudhost.Error{Code: code, Message: message, English: english}
}
