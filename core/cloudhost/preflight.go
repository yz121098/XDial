package cloudhost

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Fixed locations owned by XDial on the server. Nothing outside these paths,
// the package it may install and an optional ufw rule is changed.
const (
	serviceName              = "xdial-proxy"
	configDir                = "/etc/xdial-proxy"
	configPath               = configDir + "/config.json"
	statePath                = configDir + "/state.json"
	unitPath                 = "/etc/systemd/system/" + serviceName + ".service"
	aptKeyPath               = "/etc/apt/keyrings/xdial-sagernet.asc"
	aptSourcePath            = "/etc/apt/sources.list.d/xdial-sagernet.sources"
	defaultBinary            = "/usr/bin/sing-box"
	shadowsocksKey           = "2022-blake3-aes-128-gcm"
	ProtocolShadowsocks      = "shadowsocks"
	ProtocolVLESSReality     = "vless-reality"
	DefaultRealityServerName = "www.microsoft.com"
	vlessVisionFlow          = "xtls-rprx-vision"
	vlessDefaultFingerprint  = "chrome"
	portRangeLow             = 20000
	portRangeHigh            = 59999
	minFreeKB                = 100 * 1024
)

// Issue is one finding of the read-only inspection. Blocking issues stop setup.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	English string `json:"message_en"`
}

func issue(code, message, english string) Issue {
	return Issue{Code: code, Message: message, English: english}
}

// Existing describes an XDial proxy a previous setup left on this server.
type Existing struct {
	Protocol   string `json:"protocol,omitempty"`
	Port       int    `json:"port"`
	Method     string `json:"method,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	Password   string `json:"-"`
	UUID       string `json:"-"`
	Flow       string `json:"-"`
	PrivateKey string `json:"-"`
	PublicKey  string `json:"-"`
	ShortID    string `json:"-"`
	Active     bool   `json:"active"`
}

// Report is what the user reviews before anything changes.
type Report struct {
	HostKey         HostKey   `json:"host_key"`
	System          string    `json:"system"`
	Architecture    string    `json:"architecture"`
	Privilege       string    `json:"privilege"`
	SingBoxVersion  string    `json:"sing_box_version,omitempty"`
	SingBoxPackaged bool      `json:"sing_box_packaged"`
	Existing        *Existing `json:"existing,omitempty"`
	UFWActive       bool      `json:"ufw_active"`
	// Interrupted means an earlier setup stopped mid-way (connection lost or
	// app closed); the next run undoes what it left before continuing.
	Interrupted    bool    `json:"interrupted"`
	ListeningPorts []int   `json:"listening_ports"`
	SuggestedPort  int     `json:"suggested_port"`
	Blocking       []Issue `json:"blocking"`
	Notes          []Issue `json:"notes"`
}

type facts map[string]string

const factsScript = `
emit() { printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr '\n\r=' '   ' | cut -c1-400)"; }
raw() { printf '%s=%s\n' "$1" "$2"; }
if [ -r /etc/os-release ]; then . /etc/os-release; fi
emit os_id "${ID:-}"
emit os_like "${ID_LIKE:-}"
emit os_version "${VERSION_ID:-}"
emit os_name "${PRETTY_NAME:-}"
emit arch "$(uname -m)"
emit pid1 "$(cat /proc/1/comm 2>/dev/null)"
emit uid "$(id -u)"
emit systemctl "$(command -v systemctl)"
emit apt "$(command -v apt-get)"
emit dpkg_singbox "$(dpkg-query -W -f='${Status}|${Version}' sing-box 2>/dev/null)"
emit singbox_path "$(command -v sing-box)"
emit singbox_version "$(sing-box version 2>/dev/null | head -n1)"
emit singbox_service "$(systemctl is-active sing-box 2>/dev/null)"
emit xdial_service "$(systemctl is-active ` + serviceName + ` 2>/dev/null)"
emit xdial_unit "$(test -f ` + unitPath + ` && echo yes)"
raw xdial_state "$(base64 ` + statePath + ` 2>/dev/null | tr -d '\n')"
raw xdial_config "$(base64 ` + configPath + ` 2>/dev/null | tr -d '\n')"
emit sagernet_sources "$(grep -ls 'deb.sagernet.org' /etc/apt/sources.list /etc/apt/sources.list.d/* 2>/dev/null | tr '\n' ' ')"
emit ufw "$(ufw status 2>/dev/null | head -n1)"
emit firewalld "$(firewall-cmd --state 2>/dev/null)"
emit disk_kb "$(df -Pk / 2>/dev/null | awk 'NR==2{print $4}')"
emit listening "$(ss -Hltun 2>/dev/null | awk '{print $5}' | tr '\n' ' ')"
`

func parseFacts(output []byte) facts {
	result := facts{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 64*1024), maxOutputBytes)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			result[key] = strings.TrimSpace(value)
		}
	}
	return result
}

// Inspect reads the server without changing it.
func (s *Session) Inspect(ctx context.Context) (Report, error) {
	if err := s.detectPrivilege(ctx); err != nil {
		return Report{}, err
	}
	var result commandResult
	var err error
	if s.privilege == privilegeNone {
		result, err = s.run(ctx, factsScript, nil)
	} else {
		result, err = s.runPrivileged(ctx, factsScript, nil)
	}
	if err != nil {
		return Report{}, err
	}
	if s.privilege == privilegeSudoPassword && result.code != 0 && len(result.stdout) == 0 {
		return Report{}, newError("sudo_failed", "sudo 没有接受登录密码", "sudo did not accept the login password")
	}
	s.facts = parseFacts(result.stdout)
	report := evaluate(s.facts, s.privilege)
	report.HostKey = s.hostKey
	s.report = &report
	return report, nil
}

func evaluate(f facts, p privilege) Report {
	report := Report{
		System:       f["os_name"],
		Architecture: f["arch"],
		Privilege:    p.String(),
	}
	if report.System == "" {
		report.System = strings.TrimSpace(f["os_id"] + " " + f["os_version"])
	}
	block := func(code, message, english string) {
		report.Blocking = append(report.Blocking, issue(code, message, english))
	}
	note := func(code, message, english string) {
		report.Notes = append(report.Notes, issue(code, message, english))
	}

	if !supportedSystem(f["os_id"], f["os_version"]) {
		block("unsupported_system",
			"目前只支持 Ubuntu 20.04 及以上和 Debian 12 及以上；这台服务器是 "+orUnknown(report.System)+"。",
			"Only Ubuntu 20.04+ and Debian 12+ are supported; this server runs "+orUnknown(report.System)+".")
	}
	if arch := f["arch"]; arch != "x86_64" && arch != "aarch64" {
		block("unsupported_arch", "目前只支持 amd64 和 arm64 服务器，这台是 "+orUnknown(arch)+"。",
			"Only amd64 and arm64 servers are supported; this one is "+orUnknown(arch)+".")
	}
	if f["pid1"] != "systemd" || f["systemctl"] == "" {
		block("no_systemd", "服务器没有用 systemd 管理服务（例如容器或精简系统），无法安装常驻服务。",
			"The server does not run systemd (for example a container), so a background service cannot be installed.")
	}
	if p == privilegeNone {
		block("no_privilege", "这个账号没有 root 权限，也不能用 sudo。请改用 root 或有 sudo 权限的账号。",
			"This account has neither root nor sudo access. Use root or an account that can use sudo.")
	}

	packaged := strings.HasPrefix(f["dpkg_singbox"], "install ok installed")
	report.SingBoxPackaged = packaged
	report.SingBoxVersion = f["singbox_version"]
	if path := f["singbox_path"]; path != "" && !safePath(path) {
		block("unusable_sing_box", "服务器上已有的 sing-box 路径无法使用："+path, "The existing sing-box path on the server cannot be used: "+path)
	}
	if f["singbox_path"] == "" {
		if f["apt"] == "" {
			block("no_apt", "服务器上没有 apt，无法从 sing-box 官方软件源安装。", "apt is not available, so sing-box cannot be installed from its official repository.")
		}
		if kb, err := strconv.Atoi(f["disk_kb"]); err == nil && kb < minFreeKB {
			block("low_disk", "系统盘剩余空间不足 100 MB。", "Less than 100 MB is free on the system disk.")
		}
	} else if !packaged {
		note("sing_box_manual", "服务器已有 sing-box（"+f["singbox_path"]+"），XDial 会直接使用它，不改动它。",
			"sing-box is already on the server ("+f["singbox_path"]+"); XDial uses it as is.")
	}
	if f["singbox_service"] == "active" {
		note("sing_box_service", "服务器上已有运行中的 sing-box 服务。XDial 另建独立服务，不会修改或停止它。",
			"A sing-box service is already running. XDial adds its own service and does not change or stop it.")
	}
	if strings.HasPrefix(f["ufw"], "Status: active") {
		report.UFWActive = true
		note("ufw_active", "服务器开着 ufw 防火墙。XDial 只为代理端口添加一条放行规则，回滚时删除。",
			"ufw is active. XDial adds one rule for the proxy port only and removes it on rollback.")
	}
	if f["firewalld"] == "running" {
		note("firewalld", "服务器运行着 firewalld，XDial 不会修改它；如果连不上，请自行放行代理端口。",
			"firewalld is running and XDial does not change it; if the proxy cannot be reached, allow its port yourself.")
	}
	note("security_group", "如果服务商有安全组或防火墙面板，需要在那里放行代理端口（TCP 和 UDP）；XDial 无法替你修改。",
		"If your provider has a security group or firewall panel, allow the proxy port (TCP and UDP) there; XDial cannot change it for you.")

	report.ListeningPorts = parseListeningPorts(f["listening"])
	if previousState(f).InProgress {
		report.Interrupted = true
		note("interrupted", "上次在这台服务器上的设置没有完成（例如网络断开或 App 被关闭），服务器上留有部分改动。继续时会先撤销它们。",
			"An earlier setup on this server did not finish (for example the connection dropped or the app was closed) and left some changes. Continuing undoes them first.")
	} else if existing := parseExisting(f["xdial_config"]); existing != nil {
		existing.Active = f["xdial_service"] == "active"
		report.Existing = existing
	} else if f["xdial_unit"] == "yes" {
		block("foreign_xdial_service", "服务器上的 xdial-proxy 服务不是这个版本的 XDial 创建的，为避免覆盖已停止。",
			"An xdial-proxy service exists that this version of XDial did not create; stopping to avoid overwriting it.")
	}
	report.SuggestedPort = suggestPort(report.ListeningPorts, report.Existing)
	return report
}

func safePath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	for _, r := range path {
		if !(r == '/' || r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func supportedSystem(id, version string) bool {
	major, minor := parseVersion(version)
	switch id {
	case "ubuntu":
		return major > 20 || (major == 20 && minor >= 4)
	case "debian":
		return major >= 12
	default:
		return false
	}
}

func parseVersion(version string) (int, int) {
	parts := strings.SplitN(strings.Trim(version, `"`), ".", 3)
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未知 (unknown)"
	}
	return value
}

func parseListeningPorts(text string) []int {
	seen := map[int]bool{}
	for _, field := range strings.Fields(text) {
		index := strings.LastIndex(field, ":")
		if index < 0 {
			continue
		}
		if port, err := strconv.Atoi(field[index+1:]); err == nil && port > 0 {
			seen[port] = true
		}
	}
	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

type serverConfig struct {
	Inbounds []struct {
		Type       string `json:"type"`
		Tag        string `json:"tag"`
		ListenPort int    `json:"listen_port"`
		Method     string `json:"method"`
		Password   string `json:"password"`
		Users      []struct {
			UUID string `json:"uuid"`
			Flow string `json:"flow"`
		} `json:"users"`
		TLS *struct {
			Enabled    bool   `json:"enabled"`
			ServerName string `json:"server_name"`
			Reality    *struct {
				Enabled    bool     `json:"enabled"`
				PrivateKey string   `json:"private_key"`
				ShortID    []string `json:"short_id"`
			} `json:"reality"`
		} `json:"tls"`
	} `json:"inbounds"`
}

func parseExisting(encoded string) *Existing {
	if encoded == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var config serverConfig
	if json.Unmarshal(raw, &config) != nil {
		return nil
	}
	for _, inbound := range config.Inbounds {
		if inbound.Type == "shadowsocks" && inbound.Tag == inboundTag && inbound.ListenPort > 0 && inbound.Password != "" {
			return &Existing{
				Protocol: ProtocolShadowsocks,
				Port:     inbound.ListenPort,
				Method:   inbound.Method,
				Password: inbound.Password,
			}
		}
		if inbound.Type == "vless" && inbound.Tag == vlessInboundTag && inbound.ListenPort > 0 &&
			len(inbound.Users) > 0 && inbound.Users[0].UUID != "" &&
			inbound.TLS != nil && inbound.TLS.Enabled && inbound.TLS.ServerName != "" &&
			inbound.TLS.Reality != nil && inbound.TLS.Reality.Enabled && inbound.TLS.Reality.PrivateKey != "" {
			privBytes, err := base64.RawURLEncoding.DecodeString(inbound.TLS.Reality.PrivateKey)
			if err != nil || len(privBytes) != 32 {
				continue
			}
			privKey, err := ecdh.X25519().NewPrivateKey(privBytes)
			if err != nil {
				continue
			}
			shortID := ""
			if len(inbound.TLS.Reality.ShortID) > 0 {
				shortID = strings.ToLower(inbound.TLS.Reality.ShortID[0])
				if len(shortID) > 16 || len(shortID)%2 != 0 {
					continue
				}
				if _, err := hex.DecodeString(shortID); err != nil {
					continue
				}
			}
			flow := inbound.Users[0].Flow
			if flow == "" {
				flow = vlessVisionFlow
			}
			return &Existing{
				Protocol:   ProtocolVLESSReality,
				Port:       inbound.ListenPort,
				ServerName: inbound.TLS.ServerName,
				UUID:       inbound.Users[0].UUID,
				Flow:       flow,
				PrivateKey: inbound.TLS.Reality.PrivateKey,
				PublicKey:  base64.RawURLEncoding.EncodeToString(privKey.PublicKey().Bytes()),
				ShortID:    shortID,
			}
		}
	}
	return nil
}

func validRealityServerName(raw string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		name = DefaultRealityServerName
	}
	if len(name) > 253 || !strings.Contains(name, ".") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return "", false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return "", false
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".internal") {
		return "", false
	}
	labels := strings.Split(name, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", false
			}
		}
	}
	return name, true
}

func suggestPort(listening []int, existing *Existing) int {
	if existing != nil {
		return existing.Port
	}
	used := map[int]bool{}
	for _, port := range listening {
		used[port] = true
	}
	span := big.NewInt(portRangeHigh - portRangeLow + 1)
	for range 64 {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			break
		}
		if port := portRangeLow + int(n.Int64()); !used[port] {
			return port
		}
	}
	for port := portRangeLow; port <= portRangeHigh; port++ {
		if !used[port] {
			return port
		}
	}
	return 0
}

// PortAvailable reports whether the port is free, or already used by XDial's own proxy.
func (r Report) PortAvailable(port int) bool {
	return r.PortAvailableForProtocol(port, ProtocolShadowsocks)
}

// PortAvailableForProtocol reports whether port is allowed and free for the chosen protocol.
// VLESS + Reality additionally permits TCP/443 when free so it can blend with normal HTTPS.
func (r Report) PortAvailableForProtocol(port int, protocol string) bool {
	allow443 := protocol == ProtocolVLESSReality && port == 443
	if !allow443 && (port < 1024 || port > 65535) {
		return false
	}
	if r.Existing != nil && r.Existing.Port == port {
		return true
	}
	for _, used := range r.ListeningPorts {
		if used == port {
			return false
		}
	}
	return true
}
