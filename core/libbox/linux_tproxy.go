//go:build linux && !android

package libbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/redirect"
	jsonext "github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// ConfigureLinuxTProxy selects a protected native-flow session before Start.
// bypassMark is a single bit reserved by the platform installation transaction.
// It must equal every traffic configuration's numeric default_mark. This
// API does not alter nftables, policy routing, DNS, or any existing interface.
func (l *Libbox) ConfigureLinuxTProxy(bypassMark uint32) error {
	if bypassMark == 0 || bypassMark&(bypassMark-1) != 0 {
		return fmt.Errorf("Linux bypass mark must be one reserved bit")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running || l.startPreparing || l.cleaning || l.stablePacketIngress {
		return fmt.Errorf("Linux transparent setup requires an idle native-flow runtime")
	}
	l.platform.mu.Lock()
	l.platform.kernelRoutedUnderlay = true
	l.platform.mu.Unlock()
	l.nativeFlowIngress = true
	l.nativeFlowConfigValidator = func(s string) error { return validateLinuxTProxyConfig(s, bypassMark) }
	return nil
}

// The initial adapter supports sing-box-owned dialers. Protocol-owned sockets
// and local DNS need their own protected capability leases before admission.
// Refusal is explicit; there is no fallback to a different Line or resolver.
func validateLinuxTProxyConfig(s string, mark uint32) error {
	var doc map[string]any
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return fmt.Errorf("invalid Linux traffic configuration")
	}
	route, _ := doc["route"].(map[string]any)
	hasMark := func(v any) bool {
		f, ok := v.(float64)
		return ok && f == float64(mark)
	}
	if route["auto_detect_interface"] == true || route["default_interface"] != nil {
		return fmt.Errorf("Linux marked sockets must preserve kernel Underlay selection")
	}
	if !hasMark(route["default_mark"]) {
		return fmt.Errorf("Linux traffic configuration is missing its bypass mark")
	}
	for _, key := range []string{"endpoints", "services", "inbounds"} {
		if values, _ := doc[key].([]any); len(values) != 0 {
			return fmt.Errorf("Linux transparent runtime does not admit %s", key)
		}
	}
	if sets, _ := route["rule_set"].([]any); len(sets) != 0 {
		for _, raw := range sets {
			entry, _ := raw.(map[string]any)
			if entry["type"] == "remote" {
				return fmt.Errorf("Linux rules must be prepared before connection")
			}
		}
	}
	outbounds, _ := doc["outbounds"].([]any)
	for _, raw := range outbounds {
		v, _ := raw.(map[string]any)
		switch v["type"] {
		case "direct", "socks", "shadowsocks", "trojan", "vmess", "vless", "anytls", "selector", "urltest":
		default:
			return fmt.Errorf("Linux outbound requires a protected protocol capability")
		}
	}
	dns, _ := doc["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	for _, raw := range servers {
		v, _ := raw.(map[string]any)
		switch v["type"] {
		case "hosts", "udp", "tls", "https":
		default:
			return fmt.Errorf("Linux DNS requires a protected sing-box transport")
		}
	}
	var inspect func(any) error
	inspect = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for k, item := range v {
				if k == "routing_mark" && !hasMark(item) {
					return fmt.Errorf("Linux explicit socket mark removes bypass protection")
				}
				if k == "netns" && item != "" {
					return fmt.Errorf("Linux socket namespace requires a separate protected adapter")
				}
				if err := inspect(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range v {
				if err := inspect(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(doc)
}

type linuxTProxyListener struct {
	box    *box.Box
	cancel context.CancelFunc
	once   sync.Once
}

func (p *linuxTProxyListener) Close() error {
	p.once.Do(func() { p.cancel(); _ = p.box.Close() })
	return nil
}

// StartLinuxTProxy starts only the stable kernel-facing listener. Callers must
// install their owned interception rules afterwards and remove them before Stop.
// All original destinations and UDP reply sockets come from sing-box's upstream
// TProxy adapter. No client-supplied process identity enters this boundary.
func (l *Libbox) StartLinuxTProxy(address string, bypassMark uint32) error {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Port() == 0 {
		return fmt.Errorf("Linux transparent listener requires an explicit loopback endpoint")
	}
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	l.mu.Lock()
	if !l.nativeFlowIngress || !l.running || l.cleaning || l.packetIngress == nil || l.nativeFlowConfigValidator == nil {
		l.mu.Unlock()
		return fmt.Errorf("protected Linux native-flow runtime is not running")
	}
	// Check the supplied listener mark against the configured session guard.
	check := fmt.Sprintf(`{"route":{"default_mark":%d}}`, bypassMark)
	if err := l.nativeFlowConfigValidator(check); err != nil {
		l.mu.Unlock()
		return err
	}
	p, platform := l.packetIngress, l.platform
	l.runtimeUsers.Add(1)
	l.mu.Unlock()
	defer l.runtimeUsers.Done()
	p.mu.Lock()
	if p.disabled || p.nativeListenerStarting || p.nativeListener != nil {
		p.mu.Unlock()
		return fmt.Errorf("Linux transparent listener is unavailable or already started")
	}
	p.nativeListenerStarting = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.nativeListenerStarting = false; p.mu.Unlock() }()
	ctx, cancel := context.WithCancel(context.Background()) // independent of a retiring data generation
	registry := inboundRegistry()
	inbound.Register[option.TProxyInboundOptions](registry, "tproxy", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, opts option.TProxyInboundOptions) (adapter.Inbound, error) {
		return redirect.NewTProxy(ctx, &linuxTProxyRouter{packetIngressRouter: &packetIngressRouter{router, p}, endpoint: endpoint}, logger, tag, opts)
	})
	ctx = boxContext(ctx)
	ctx = service.ContextWith[option.InboundOptionsRegistry](ctx, registry)
	ctx = service.ContextWith[adapter.InboundRegistry](ctx, registry)
	ctx = service.ContextWith[adapter.PlatformInterface](ctx, platform.lineRuntimeCapability())
	raw := fmt.Sprintf(`{"log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"unused"}],"final":"unused"},"outbounds":[{"type":"direct","tag":"unused"}],"route":{"final":"unused"},"inbounds":[{"type":"tproxy","tag":"linux-transparent","listen":%q,"listen_port":%d,"routing_mark":%d}]}`, endpoint.Addr().String(), endpoint.Port(), bypassMark)
	options, err := jsonext.UnmarshalExtendedContext[option.Options](ctx, []byte(raw))
	if err != nil {
		cancel()
		return err
	}
	b, err := box.New(box.Options{Options: options, Context: ctx})
	if err != nil {
		cancel()
		return err
	}
	listener := &linuxTProxyListener{box: b, cancel: cancel}
	if err := b.Start(); err != nil {
		listener.Close()
		return err
	}
	p.mu.Lock()
	if p.disabled {
		p.mu.Unlock()
		listener.Close()
		return net.ErrClosed
	}
	p.nativeListener = listener
	p.mu.Unlock()
	return nil
}

type linuxTProxyRouter struct {
	*packetIngressRouter
	endpoint netip.AddrPort
}

func (r *linuxTProxyRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, close N.CloseHandlerFunc) {
	if metadata.Destination.AddrPort() == r.endpoint {
		conn.Close()
		if close != nil {
			close(fmt.Errorf("direct connection to transparent listener rejected"))
		}
		return
	}
	r.packetIngressRouter.RouteConnectionEx(ctx, conn, metadata, close)
}
func (r *linuxTProxyRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, close N.CloseHandlerFunc) {
	if metadata.Destination.AddrPort() == r.endpoint {
		conn.Close()
		if close != nil {
			close(fmt.Errorf("direct packet to transparent listener rejected"))
		}
		return
	}
	r.packetIngressRouter.RoutePacketConnectionEx(ctx, conn, metadata, close)
}
