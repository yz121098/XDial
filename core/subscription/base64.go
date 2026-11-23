package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/kafeifei/xdial/core/config"
)

// IsShareURI reports whether text begins with a supported proxy share link scheme.
func IsShareURI(text string) bool {
	trimmed := strings.TrimSpace(text)
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"vless://", "ss://", "trojan://", "vmess://", "anytls://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// ParseShareURIs parses one or more proxy share links (raw text or Base64-encoded)
// and validates that every returned line can construct a usable outbound.
func ParseShareURIs(content string) ([]config.Line, error) {
	lines, err := parseBase64(content)
	if err != nil {
		return nil, fmt.Errorf("未识别到有效的节点分享链接（支持 vless://、ss://、trojan://、vmess://、anytls://）")
	}
	usable := make([]config.Line, 0, len(lines))
	for i := range lines {
		if lines[i].Type == config.LineTypeAnyTLS && lines[i].TFO {
			lines[i].TFO = false
		}
		if !config.LineHasUsableOutbound(&lines[i]) {
			return nil, fmt.Errorf("第 %d 条 %s 分享链接参数不完整或不受支持", i+1, lines[i].Type)
		}
		usable = append(usable, lines[i])
	}
	return usable, nil
}

func parseBase64(content string) ([]config.Line, error) {
	decoded := tryBase64Decode(strings.TrimSpace(content))
	decoded = strings.ReplaceAll(decoded, "\r\n", "\n")
	textLines := strings.Split(decoded, "\n")

	var results []config.Line
	for _, line := range textLines {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), `"'`))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		ln, ok := uriToLine(line)
		if ok {
			results = append(results, ln)
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no supported URIs found")
	}
	return results, nil
}

func tryBase64Decode(s string) string {
	// try standard base64
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(decoded)
	}
	// try URL-safe base64
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return string(decoded)
	}
	// try without padding
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return string(decoded)
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return string(decoded)
	}
	return s
}

func normalizeURIScheme(uri string) string {
	if idx := strings.Index(uri, "://"); idx > 0 {
		return strings.ToLower(uri[:idx]) + uri[idx:]
	}
	return uri
}

func uriToLine(uri string) (config.Line, bool) {
	uri = normalizeURIScheme(strings.TrimSpace(uri))
	switch {
	case strings.HasPrefix(uri, "ss://"):
		return parseSS(uri)
	case strings.HasPrefix(uri, "vmess://"):
		return parseVMess(uri)
	case strings.HasPrefix(uri, "trojan://"):
		return parseTrojan(uri)
	case strings.HasPrefix(uri, "vless://"):
		return parseVLESS(uri)
	case strings.HasPrefix(uri, "anytls://"):
		return parseAnyTLS(uri)
	default:
		return config.Line{}, false
	}
}

// ss://base64(method:password)@server:port#name
// or ss://base64(method:password@server:port)#name
func parseSS(uri string) (config.Line, bool) {
	uri = strings.TrimPrefix(uri, "ss://")

	name := ""
	if i := strings.LastIndex(uri, "#"); i >= 0 {
		name, _ = url.QueryUnescape(uri[i+1:])
		uri = uri[:i]
	}

	var method, password, server string
	var port int

	if i := strings.Index(uri, "@"); i >= 0 {
		userInfo := tryBase64Decode(uri[:i])
		hostPort := uri[i+1:]
		parts := strings.SplitN(userInfo, ":", 2)
		if len(parts) != 2 {
			return config.Line{}, false
		}
		method = parts[0]
		password = parts[1]
		server, port = parseHostPort(hostPort)
	} else {
		decoded := tryBase64Decode(uri)
		if i := strings.Index(decoded, "@"); i >= 0 {
			parts := strings.SplitN(decoded[:i], ":", 2)
			if len(parts) != 2 {
				return config.Line{}, false
			}
			method = parts[0]
			password = parts[1]
			server, port = parseHostPort(decoded[i+1:])
		} else {
			return config.Line{}, false
		}
	}

	if server == "" || port == 0 {
		return config.Line{}, false
	}
	if name == "" {
		name = server
	}

	return config.Line{
		ID:       shortID(),
		Name:     name,
		Type:     config.LineTypeShadowsocks,
		Enabled:  true,
		SSServer: server,
		SSPort:   port,
		SSMethod: method,
		SSPass:   password,
	}, true
}

// vmess://base64(json)
// v2rayN format: {"v":"2","ps":"name","add":"server","port":"443","id":"uuid","aid":"0",...}
func parseVMess(uri string) (config.Line, bool) {
	encoded := strings.TrimPrefix(uri, "vmess://")
	decoded := tryBase64Decode(encoded)

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(decoded), &obj); err != nil {
		return config.Line{}, false
	}

	server := getString(obj, "add")
	portStr := getString(obj, "port")
	uuid := getString(obj, "id")
	name := getString(obj, "ps")

	port, _ := strconv.Atoi(portStr)
	if server == "" || port == 0 || uuid == "" {
		return config.Line{}, false
	}
	if name == "" {
		name = server
	}

	aid, _ := strconv.Atoi(getString(obj, "aid"))

	return config.Line{
		ID:          shortID(),
		Name:        name,
		Type:        config.LineTypeVMess,
		Enabled:     true,
		VMessServer: server,
		VMessPort:   port,
		VMessUUID:   uuid,
		VMessAltID:  aid,
	}, true
}

// trojan://password@server:port?sni=xxx#name
func parseTrojan(uri string) (config.Line, bool) {
	u, err := url.Parse(uri)
	if err != nil {
		return config.Line{}, false
	}

	password := u.User.Username()
	server := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	name, _ := url.QueryUnescape(u.Fragment)
	sni := u.Query().Get("sni")

	if server == "" || port == 0 || password == "" {
		return config.Line{}, false
	}
	if sni == "" {
		sni = server
	}
	if name == "" {
		name = server
	}

	return config.Line{
		ID:             shortID(),
		Name:           name,
		Type:           config.LineTypeTrojan,
		Enabled:        true,
		TrojanServer:   server,
		TrojanPort:     port,
		TrojanPassword: password,
		TrojanSNI:      sni,
	}, true
}

// vless://uuid@server:port?security=reality&pbk=xxx&sid=xxx&sni=xxx&fp=chrome&flow=xtls-rprx-vision#name
func parseVLESS(uri string) (config.Line, bool) {
	rawFragment := ""
	if idx := strings.LastIndex(uri, "#"); idx >= 0 {
		rawFragment = uri[idx+1:]
		uri = uri[:idx]
	}
	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return config.Line{}, false
	}

	userUUID := strings.TrimSpace(u.User.Username())
	server := strings.TrimSpace(u.Hostname())
	port, _ := strconv.Atoi(u.Port())
	name := strings.TrimSpace(rawFragment)
	if unescaped, uErr := url.PathUnescape(rawFragment); uErr == nil {
		name = strings.TrimSpace(unescaped)
	} else if unescaped, qErr := url.QueryUnescape(rawFragment); qErr == nil {
		name = strings.TrimSpace(unescaped)
	}
	query := u.Query()

	if server == "" || port == 0 || userUUID == "" {
		return config.Line{}, false
	}
	if name == "" {
		name = server
	}

	if enc := strings.ToLower(strings.TrimSpace(query.Get("encryption"))); enc != "" && enc != "none" {
		return config.Line{}, false
	}
	if netType := strings.ToLower(strings.TrimSpace(firstQueryValue(query, "type", "network"))); netType != "" && netType != "tcp" && netType != "raw" {
		return config.Line{}, false
	}
	if headerType := strings.ToLower(strings.TrimSpace(query.Get("headerType"))); headerType != "" && headerType != "none" {
		return config.Line{}, false
	}

	security := strings.ToLower(strings.TrimSpace(query.Get("security")))
	publicKey := strings.TrimSpace(firstQueryValue(query, "pbk", "public_key", "public-key", "publicKey"))
	shortIDVal := strings.TrimSpace(firstQueryValue(query, "sid", "short_id", "short-id", "shortId"))
	switch security {
	case "reality":
		if publicKey == "" {
			return config.Line{}, false
		}
	case "tls":
		if publicKey != "" || shortIDVal != "" {
			return config.Line{}, false
		}
	case "":
		if publicKey == "" {
			return config.Line{}, false
		}
	default:
		return config.Line{}, false
	}

	sni := strings.TrimSpace(firstQueryValue(query, "sni", "peer", "server_name", "servername"))
	if publicKey == "" && sni == "" {
		sni = server
	}
	fingerprint := normalizeAnyTLSFingerprint(firstQueryValue(
		query,
		"fp",
		"fingerprint",
		"client-fingerprint",
		"client_fingerprint",
	))
	if publicKey != "" && fingerprint == "" {
		fingerprint = "chrome"
	}

	alpn, ok := anyTLSQueryALPN(query)
	if !ok {
		return config.Line{}, false
	}

	line := config.Line{
		ID:                     shortID(),
		Name:                   name,
		Type:                   config.LineTypeVLESS,
		Enabled:                true,
		VLESSServer:            server,
		VLESSPort:              port,
		VLESSUUID:              userUUID,
		VLESSFlow:              strings.TrimSpace(query.Get("flow")),
		VLESSSNI:               sni,
		VLESSClientFingerprint: fingerprint,
		VLESSALPN:              alpn,
		VLESSRealityPublicKey:  publicKey,
		VLESSRealityShortID:    shortIDVal,
		VLESSPacketEncoding:    strings.ToLower(strings.TrimSpace(firstQueryValue(query, "packetEncoding", "packet-encoding", "packet_encoding"))),
		UDP:                    true,
	}
	line.AllowInsecure, ok = anyTLSQueryBool(
		query,
		"insecure",
		"allow_insecure",
		"allowInsecure",
		"skip-cert-verify",
	)
	if !ok {
		return config.Line{}, false
	}
	if _, hasUDP := firstQueryEntry(query, "udp", "udp-relay"); hasUDP {
		line.UDP, ok = anyTLSQueryBool(query, "udp", "udp-relay")
		if !ok {
			return config.Line{}, false
		}
	}
	line.TFO, ok = anyTLSQueryBool(query, "tfo")
	if !ok {
		return config.Line{}, false
	}
	if !config.LineHasUsableOutbound(&line) {
		return config.Line{}, false
	}
	return line, true
}

// anytls://password@server:port?sni=xxx&insecure=1#name
func parseAnyTLS(uri string) (config.Line, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return config.Line{}, false
	}

	password := u.User.Username()
	if password == "" {
		password, _ = u.User.Password()
	}
	server := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	name, _ := url.QueryUnescape(u.Fragment)
	query := u.Query()
	sni := firstQueryValue(query, "sni", "peer", "server_name", "servername")

	if server == "" || port == 0 || password == "" {
		return config.Line{}, false
	}
	if sni == "" {
		sni = server
	}
	if name == "" {
		name = server
	}

	fingerprint := normalizeAnyTLSFingerprint(firstQueryValue(
		query,
		"client-fingerprint",
		"client_fingerprint",
		"fingerprint",
		"fp",
	))
	alpn, ok := anyTLSQueryALPN(query)
	if !ok {
		return config.Line{}, false
	}

	line := config.Line{
		ID:                      shortID(),
		Name:                    name,
		Type:                    config.LineTypeAnyTLS,
		Enabled:                 true,
		AnyTLSServer:            server,
		AnyTLSPort:              port,
		AnyTLSPassword:          password,
		AnyTLSSNI:               sni,
		AnyTLSClientFingerprint: fingerprint,
		AnyTLSALPN:              alpn,
	}
	if value, exists := firstQueryEntry(
		query,
		"idle-session-check-interval",
		"idle_session_check_interval",
	); exists {
		seconds, valid := parseAnyTLSSessionSeconds(value)
		if !valid {
			return config.Line{}, false
		}
		line.AnyTLSIdleSessionCheckInterval = seconds
	}
	if value, exists := firstQueryEntry(
		query,
		"idle-session-timeout",
		"idle_session_timeout",
	); exists {
		seconds, valid := parseAnyTLSSessionSeconds(value)
		if !valid {
			return config.Line{}, false
		}
		line.AnyTLSIdleSessionTimeout = seconds
	}
	if value, exists := firstQueryEntry(
		query,
		"min-idle-session",
		"min_idle_session",
	); exists {
		count, valid := parseAnyTLSMinIdleSession(value)
		if !valid {
			return config.Line{}, false
		}
		line.AnyTLSMinIdleSession = count
	}

	line.AllowInsecure, ok = anyTLSQueryBool(
		query,
		"insecure",
		"allow_insecure",
		"allowInsecure",
		"skip-cert-verify",
	)
	if !ok {
		return config.Line{}, false
	}
	line.UDP, ok = anyTLSQueryBool(query, "udp", "udp-relay")
	if !ok {
		return config.Line{}, false
	}
	if !config.LineHasUsableOutbound(&line) {
		return config.Line{}, false
	}
	line.TFO, ok = anyTLSQueryBool(query, "tfo")
	if !ok {
		return config.Line{}, false
	}
	return line, true
}

func firstQueryValue(values url.Values, keys ...string) string {
	value, _ := firstQueryEntry(values, keys...)
	return value
}

func firstQueryEntry(values url.Values, keys ...string) (string, bool) {
	for _, key := range keys {
		if entries, exists := values[key]; exists && len(entries) > 0 {
			return entries[0], true
		}
	}
	return "", false
}

func anyTLSQueryBool(values url.Values, keys ...string) (bool, bool) {
	value, exists := firstQueryEntry(values, keys...)
	if !exists {
		return false, true
	}
	return parseOptionalBool(value)
}

func anyTLSQueryALPN(values url.Values) ([]string, bool) {
	rawValues, exists := values["alpn"]
	if !exists {
		return nil, true
	}
	var protocols []string
	for _, rawValue := range rawValues {
		items, valid := splitAnyTLSALPN(rawValue)
		if !valid {
			return nil, false
		}
		protocols = append(protocols, items...)
	}
	return normalizeAnyTLSALPN(protocols), true
}

func parseHostPort(s string) (string, int) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		host := s[:i]
		port, _ := strconv.Atoi(s[i+1:])
		return host, port
	}
	return s, 0
}
