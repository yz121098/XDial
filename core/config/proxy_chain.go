package config

import (
	"fmt"
	"net/netip"
)

// A chain is an ordered, scenario-bindable group, not a selector. The managed
// VPN can supply the first dialer but cannot itself be dialed through a proxy.
func validateProxyChain(profile *Profile, chain *Line, requireUsable bool) error {
	if len(chain.GroupMembers) < 2 || chain.GroupDefault != "" || len(chain.NativeOptions) != 0 {
		return fmt.Errorf("proxy chain %q requires at least two ordered hops and no selector or native overrides", chain.ID)
	}
	seen := map[string]bool{}
	for i, id := range chain.GroupMembers {
		hop := profile.FindLine(id)
		if hop == nil || seen[id] || (requireUsable && !hop.Enabled) {
			return fmt.Errorf("proxy chain %q: missing, disabled or repeated hop %q", chain.ID, id)
		}
		seen[id] = true
		switch hop.Type {
		case LineTypeVPN:
			if i != 0 {
				return fmt.Errorf("proxy chain %q: AnyConnect must be the first hop", chain.ID)
			}
		case LineTypeTrojan, LineTypeShadowsocks, LineTypeVMess, LineTypeVLESS, LineTypeAnyTLS:
			if requireUsable && !lineHasUsableOutbound(hop) {
				return fmt.Errorf("proxy chain %q: unusable hop %q", chain.ID, id)
			}
		default:
			return fmt.Errorf("proxy chain %q: unsupported hop %q (%s)", chain.ID, id, hop.Type)
		}
	}
	return nil
}

// Only the last hop gets the public Line tag. Private copies let the same
// Reality Line be used standalone or in different chains without mutation.
func buildChainOutbounds(profile *Profile, chain *Line) ([]map[string]interface{}, error) {
	if err := validateProxyChain(profile, chain, true); err != nil {
		return nil, err
	}
	var result []map[string]interface{}
	var previous string
	for i, id := range chain.GroupMembers {
		hop := profile.FindLine(id)
		if hop.Type == LineTypeVPN {
			previous = resolveOutboundTag(hop) // one shared process-owned tunnel
			continue
		}
		outbound := buildProxyOutbound(hop)
		tag := resolveOutboundTag(chain)
		if i != len(chain.GroupMembers)-1 {
			tag = fmt.Sprintf("chain-hop-%d-%s", i, chain.ID)
		}
		outbound["tag"] = tag
		if previous != "" {
			outbound["detour"] = previous
		}
		previous = tag
		result = append(result, outbound)
	}
	return result, nil
}

// scenarioDomainDNSMatch mirrors what is knowable at name lookup time. An
// IP-only or inseparable mixed match must not acquire a DNS rule of its own.
func scenarioDomainDNSMatch(rs *RuleSet) map[string]interface{} {
	var match map[string]interface{}
	switch rs.Type {
	case RuleSetTypeManual:
		if len(rs.Domains) > 0 && (!rs.Invert || len(rs.CIDRs) == 0) {
			match = map[string]interface{}{"domain_suffix": rs.Domains}
		}
	case RuleSetTypeNative:
		if nativeRuleDomainOnly(rs.NativeRule) {
			match = map[string]interface{}{"rule_set": sbRuleSetTag(rs)}
		}
	case RuleSetTypeURL:
		if rs.RuntimeMatchKind == RuleSetMatchDomain {
			match = map[string]interface{}{"rule_set": sbRuleSetTag(rs)}
		}
	}
	if match != nil {
		applyRuleSetInvert(match, rs)
	}
	return match
}

// Keep the entry hop's bootstrap resolver separate from user traffic DNS.
// Preserve MagicDNS priority and the order of explicit Scenario exceptions.
func configureChainDNS(dns map[string]interface{}, platform Platform, profile *Profile, scenario *Scenario, subTags map[string]string, validTags map[string]bool, final string, magicTags []string) {
	servers := dns["servers"].([]map[string]interface{})
	seen := map[string]bool{}
	for _, server := range servers {
		seen[server["tag"].(string)] = true
	}
	resolver := func(out string) string {
		if out == "direct" || selectedGroupUsesDirect(profile, out) {
			if platform.isNetworkExtension() {
				return mobilePublicDNSTag
			}
			return desktopPublicDNSTag
		}
		if out == "vpn" {
			if platform.isNetworkExtension() {
				return mobileDispatcherDNSTag
			}
			if seen[desktopEnterpriseDNSTag] {
				return desktopEnterpriseDNSTag
			}
		}
		tag := desktopProxyDNSTag(out)
		if !seen[tag] {
			servers = append(servers, map[string]interface{}{"type": "https", "tag": tag, "server": transparentProxyDoHIPv4Server, "detour": out})
			seen[tag] = true
		}
		return tag
	}
	var rules []map[string]interface{}
	for _, endpointTag := range magicTags {
		rules = append(rules, buildTailnetDNSRules(mobileTailscaleDNSTag(endpointTag))...)
	}
	for i := range scenario.Bindings {
		b := &scenario.Bindings[i]
		rs := profile.FindRuleSet(b.RuleSetID)
		out := bindingOutboundTag(profile, b, subTags, validTags)
		if rs == nil || !rs.Enabled || out == "" {
			continue
		}
		if match := scenarioDomainDNSMatch(rs); match != nil {
			match["server"] = resolver(out)
			rules = append(rules, match)
		}
	}
	dns["final"] = resolver(final)
	dns["servers"] = servers
	dns["independent_cache"] = true
	if len(rules) != 0 {
		dns["rules"] = rules
	} else {
		delete(dns, "rules")
	}
}

// The external desktop SOCKS bridge otherwise resolves FQDNs with the host OS.
// Resolve only the VPN's next-hop endpoint via TCP DNS *inside* that bridge.
func configureDesktopChainBootstrap(outbounds []map[string]interface{}, dns map[string]interface{}, vpnDNS []string) error {
	const tag = "chain-vpn-bootstrap"
	added := false
	for _, ob := range outbounds {
		if ob["detour"] != "vpn" {
			continue
		}
		host, _ := ob["server"].(string)
		if _, err := netip.ParseAddr(host); err == nil {
			continue
		}
		if !added {
			server := ""
			for _, raw := range vpnDNS {
				if ip, err := netip.ParseAddr(raw); err == nil && ip.Is4() && !ip.IsUnspecified() {
					server = ip.String()
					break
				}
			}
			if server == "" {
				return fmt.Errorf("proxy chain requires tunnel DNS to resolve the hop after AnyConnect")
			}
			dns["servers"] = append(dns["servers"].([]map[string]interface{}), map[string]interface{}{"tag": tag, "type": "tcp", "server": server, "detour": "vpn"})
			added = true
		}
		ob["domain_resolver"] = map[string]interface{}{"server": tag, "strategy": "ipv4_only"}
	}
	return nil
}
