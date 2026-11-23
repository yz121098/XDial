package cloudhost

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The sing-box project's APT signing key (Project S <contact@sagernet.org>,
// fingerprint 2C31 7FBD 5D88 6B4E 89BA E8DA 6D91 5217 2A2B 2F0C), shipped with
// XDial instead of downloaded by the server, so apt verifies every package
// against a key the user's device already trusts.
//
//go:embed sagernet.asc
var sagernetKey []byte

const (
	inboundTag      = "xdial-ss"
	vlessInboundTag = "xdial-vless"
)

// Options configures the proxy protocol, port and Reality camouflage domain.
type Options struct {
	Protocol   string `json:"protocol,omitempty"`
	Port       int    `json:"port,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func normalizeOptions(opts Options) (Options, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Protocol)) {
	case "", ProtocolShadowsocks:
		opts.Protocol = ProtocolShadowsocks
		opts.ServerName = ""
	case ProtocolVLESSReality, "vless":
		opts.Protocol = ProtocolVLESSReality
		serverName, ok := validRealityServerName(opts.ServerName)
		if !ok {
			return Options{}, newError("invalid_server_name",
				"请填写有效的伪装域名（如 www.microsoft.com），不能填写 IP 地址。",
				"Enter a valid camouflage domain name (such as www.microsoft.com), not an IP address.")
		}
		opts.ServerName = serverName
	default:
		return Options{}, newError("unsupported_protocol", "不支持的代理协议："+opts.Protocol, "Unsupported proxy protocol: "+opts.Protocol)
	}
	return opts, nil
}

// Step is one planned change, shown to the user before anything happens.
type Step struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	English string `json:"title_en"`
	Detail  string `json:"detail,omitempty"`
}

// Plan is the exact set of changes setup will make.
type Plan struct {
	Protocol   string `json:"protocol,omitempty"`
	Port       int    `json:"port"`
	Method     string `json:"method,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	Reuse      bool   `json:"reuse"`
	Recover    bool   `json:"recover"`
	Steps      []Step `json:"steps"`
	binary     string
	install    bool
	source     string
	ufw        bool
}

// Result is what the client saves as a line.
type Result struct {
	Protocol    string `json:"protocol,omitempty"`
	Server      string `json:"server"`
	Port        int    `json:"port"`
	Method      string `json:"method,omitempty"`
	Password    string `json:"password,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	Flow        string `json:"flow,omitempty"`
	ServerName  string `json:"server_name,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`
	ShortID     string `json:"short_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Reused      bool   `json:"reused"`
}

// Event reports progress; it never contains secrets.
type Event struct {
	Step    string `json:"step"`
	Status  string `json:"status"` // running, done, failed, rolling_back, rolled_back
	Message string `json:"message,omitempty"`
}

// Plan returns the changes setup would make for the port (0 picks the suggested one).
func (s *Session) Plan(port int) (Plan, error) {
	return s.PlanWithOptions(Options{Protocol: ProtocolShadowsocks, Port: port})
}

// PlanWithOptions returns the changes setup would make for the chosen protocol and port.
func (s *Session) PlanWithOptions(opts Options) (Plan, error) {
	if s.report == nil {
		return Plan{}, newError("not_inspected", "请先检查服务器", "Inspect the server first")
	}
	return BuildPlanWithOptions(*s.report, s.facts, opts)
}

// BuildPlan turns the inspection into concrete changes for the chosen port.
func BuildPlan(report Report, facts facts, port int) (Plan, error) {
	return BuildPlanWithOptions(report, facts, Options{Protocol: ProtocolShadowsocks, Port: port})
}

// BuildPlanWithOptions turns the inspection into concrete changes for the chosen protocol, port and SNI.
func BuildPlanWithOptions(report Report, facts facts, opts Options) (Plan, error) {
	if len(report.Blocking) > 0 {
		return Plan{}, newError("blocked", report.Blocking[0].Message, report.Blocking[0].English)
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return Plan{}, err
	}
	port := normalized.Port
	if port == 0 {
		port = report.SuggestedPort
	}
	if !report.PortAvailableForProtocol(port, normalized.Protocol) {
		if normalized.Protocol == ProtocolVLESSReality {
			return Plan{}, newError("port_in_use",
				"端口 "+strconv.Itoa(port)+" 已被占用或不可用，请换用空闲的 443 或 1024–65535 端口。",
				"Port "+strconv.Itoa(port)+" is in use or not allowed; choose 443 (if free) or a free port between 1024 and 65535.")
		}
		return Plan{}, newError("port_in_use",
			"端口 "+strconv.Itoa(port)+" 已被占用或不可用，请换一个 1024–65535 之间的空闲端口。",
			"Port "+strconv.Itoa(port)+" is in use or not allowed; choose a free port between 1024 and 65535.")
	}
	plan := Plan{
		Protocol:   normalized.Protocol,
		Port:       port,
		ServerName: normalized.ServerName,
		binary:     facts["singbox_path"],
		ufw:        report.UFWActive,
	}
	if normalized.Protocol == ProtocolShadowsocks {
		plan.Method = shadowsocksKey
	} else {
		plan.Method = "vless-reality"
	}
	sources := strings.Fields(facts["sagernet_sources"])
	if report.Interrupted {
		// Undo the interrupted run first; whatever it created counts as absent.
		interrupted := previousState(facts)
		plan.Recover = true
		plan.Steps = append(plan.Steps, Step{ID: "recover", Title: "先撤销上次中断的设置留在服务器上的改动", English: "First undo what the interrupted setup left on the server"})
		if interrupted.created("package") {
			plan.binary = ""
		}
		if interrupted.created("apt_source") {
			kept := sources[:0]
			for _, source := range sources {
				if source != aptSourcePath {
					kept = append(kept, source)
				}
			}
			sources = kept
		}
	}
	if existing := report.Existing; existing != nil && existingMatchesOptions(existing, normalized, port) {
		plan.Reuse = true
		if existing.Method != "" {
			plan.Method = existing.Method
		}
		if !existing.Active {
			plan.Steps = append(plan.Steps, Step{ID: "start", Title: "重新启动已有的 XDial 代理服务", English: "Restart the existing XDial proxy service"})
		}
		if report.UFWActive {
			plan.Steps = append(plan.Steps, ufwStepForProtocol(port, normalized.Protocol))
		}
		return plan, nil
	}
	if plan.binary == "" {
		plan.install = true
		plan.binary = defaultBinary
		if len(sources) > 0 {
			plan.source = sources[0]
		} else {
			plan.Steps = append(plan.Steps,
				Step{ID: "apt_key", Title: "添加 sing-box 官方软件源的签名密钥", English: "Add the signing key of the official sing-box repository", Detail: aptKeyPath},
				Step{ID: "apt_source", Title: "添加 sing-box 官方软件源", English: "Add the official sing-box repository", Detail: aptSourcePath + " (https://deb.sagernet.org/)"},
			)
		}
		plan.Steps = append(plan.Steps, Step{ID: "install", Title: "用 apt 安装 sing-box（校验软件包签名）", English: "Install sing-box with apt (package signatures verified)", Detail: "apt-get install sing-box"})
	}
	if report.Existing != nil {
		if normalized.Protocol == ProtocolVLESSReality {
			plan.Steps = append(plan.Steps, Step{
				ID:      "config",
				Title:   "把已有的 XDial 代理更新为 VLESS + Reality（端口 " + strconv.Itoa(port) + "，伪装域名 " + normalized.ServerName + "，新生成密钥）",
				English: "Update the existing XDial proxy to VLESS + Reality on port " + strconv.Itoa(port) + " (SNI " + normalized.ServerName + ") with fresh keys",
				Detail:  configPath,
			})
		} else {
			plan.Steps = append(plan.Steps, Step{ID: "config", Title: "把已有的 XDial 代理改到端口 " + strconv.Itoa(port) + "，并换一个新密钥", English: "Move the existing XDial proxy to port " + strconv.Itoa(port) + " with a new key", Detail: configPath})
		}
	} else {
		if normalized.Protocol == ProtocolVLESSReality {
			plan.Steps = append(plan.Steps,
				Step{
					ID:      "config",
					Title:   "写入代理配置：VLESS + Reality（Vision），端口 " + strconv.Itoa(port) + "，伪装域名 " + normalized.ServerName + "，新生成 X25519 密钥与 UUID",
					English: "Write the proxy configuration: VLESS + Reality (Vision) on port " + strconv.Itoa(port) + ", SNI " + normalized.ServerName + ", with fresh X25519 keys and UUID",
					Detail:  configPath,
				},
				Step{ID: "unit", Title: "添加独立的系统服务 xdial-proxy", English: "Add a separate system service, xdial-proxy", Detail: unitPath},
			)
		} else {
			plan.Steps = append(plan.Steps,
				Step{ID: "config", Title: "写入代理配置：Shadowsocks 2022，端口 " + strconv.Itoa(port) + "，新生成的密钥", English: "Write the proxy configuration: Shadowsocks 2022 on port " + strconv.Itoa(port) + " with a new key", Detail: configPath},
				Step{ID: "unit", Title: "添加独立的系统服务 xdial-proxy", English: "Add a separate system service, xdial-proxy", Detail: unitPath},
			)
		}
	}
	if report.UFWActive {
		plan.Steps = append(plan.Steps, ufwStepForProtocol(port, normalized.Protocol))
	}
	plan.Steps = append(plan.Steps, Step{ID: "start", Title: "启动服务并确认它在端口 " + strconv.Itoa(port) + " 上监听", English: "Start the service and confirm it listens on port " + strconv.Itoa(port)})
	return plan, nil
}

func existingMatchesOptions(existing *Existing, opts Options, port int) bool {
	if existing == nil || existing.Port != port {
		return false
	}
	existingProto := existing.Protocol
	if existingProto == "" {
		existingProto = ProtocolShadowsocks
	}
	if existingProto != opts.Protocol {
		return false
	}
	if opts.Protocol == ProtocolVLESSReality {
		return strings.EqualFold(existing.ServerName, opts.ServerName)
	}
	return true
}

func ufwProtocols(protocol string) []string {
	if protocol == ProtocolVLESSReality {
		return []string{"tcp"}
	}
	return []string{"tcp", "udp"}
}

func ufwStep(port int) Step {
	return ufwStepForProtocol(port, ProtocolShadowsocks)
}

func ufwStepForProtocol(port int, protocol string) Step {
	p := strconv.Itoa(port)
	if protocol == ProtocolVLESSReality {
		return Step{ID: "ufw", Title: "在 ufw 里只放行端口 " + p + "（TCP）", English: "Allow only port " + p + " (TCP) in ufw", Detail: "ufw allow " + p + "/tcp"}
	}
	return Step{ID: "ufw", Title: "在 ufw 里只放行端口 " + p + "（TCP 和 UDP）", English: "Allow only port " + p + " (TCP and UDP) in ufw", Detail: "ufw allow " + p + "/tcp, " + p + "/udp"}
}

// state records what setup created, so a later run reuses it and rollback
// removes only XDial's own resources.
type state struct {
	Version          int   `json:"version"`
	InstalledPackage bool  `json:"installed_package"`
	AddedSource      bool  `json:"added_source"`
	UFWRule          int   `json:"ufw_rule,omitempty"`
	CreatedAt        int64 `json:"created_at"`
	// InProgress is set while a run is changing the server and cleared when it
	// finishes or is fully undone. If the connection drops or the app is closed
	// mid-way, it stays set and Created lists what that run had already made,
	// so the next run can undo exactly those resources first.
	InProgress bool     `json:"in_progress,omitempty"`
	Created    []string `json:"created,omitempty"`
	Baseline   *state   `json:"baseline,omitempty"`
	WasActive  bool     `json:"was_active,omitempty"`
	OwnedUFW   []string `json:"owned_ufw,omitempty"`
}

func (st state) created(resource string) bool {
	for _, item := range st.Created {
		if item == resource {
			return true
		}
	}
	return false
}

type undo struct {
	step string
	run  func(context.Context) error
}

// Deploy applies the plan. Any failure or cancellation rolls back what this run did.
func (s *Session) Deploy(ctx context.Context, port int, progress func(Event)) (Result, error) {
	return s.DeployWithOptions(ctx, Options{Protocol: ProtocolShadowsocks, Port: port}, progress)
}

// DeployWithOptions applies the plan for the chosen protocol, port and Reality SNI.
func (s *Session) DeployWithOptions(ctx context.Context, opts Options, progress func(Event)) (Result, error) {
	if progress == nil {
		progress = func(Event) {}
	}
	if s.report == nil {
		return Result{}, newError("not_inspected", "请先检查服务器", "Inspect the server first")
	}
	report, facts := *s.report, s.facts
	plan, err := BuildPlanWithOptions(report, facts, opts)
	if err != nil {
		return Result{}, err
	}
	if plan.Recover {
		progress(Event{Step: "recover", Status: "running"})
		if _, err := s.recoverInterrupted(ctx, previousState(facts)); err != nil {
			return Result{}, incompleteUndo()
		}
		// Recovery changes the resource baseline. Never deploy from the stale inspection.
		if report, err = s.Inspect(ctx); err != nil {
			return Result{}, err
		}
		facts = s.facts
		if plan, err = BuildPlanWithOptions(report, facts, opts); err != nil {
			return Result{}, err
		}
		progress(Event{Step: "recover", Status: "done"})
	}

	var (
		password   string
		userUUID   string
		flow       string
		privateKey string
		publicKey  string
		shortID    string
	)
	if plan.Protocol == ProtocolVLESSReality {
		if plan.Reuse {
			userUUID = report.Existing.UUID
			flow = report.Existing.Flow
			privateKey = report.Existing.PrivateKey
			publicKey = report.Existing.PublicKey
			shortID = report.Existing.ShortID
		} else {
			var genErr error
			userUUID, privateKey, publicKey, shortID, genErr = newVLESSRealityMaterial()
			if genErr != nil {
				return Result{}, newError("random_failed", "无法生成密钥", "Could not generate a key")
			}
			flow = vlessVisionFlow
		}
	} else {
		password, err = newShadowsocksKey()
		if err != nil {
			return Result{}, newError("random_failed", "无法生成密钥", "Could not generate a key")
		}
		if plan.Reuse {
			password = report.Existing.Password
		}
	}
	var undos []undo
	previous := previousState(facts)
	current := previous
	current.Version = 1
	current.Baseline = &previous
	current.WasActive = report.Existing != nil && report.Existing.Active
	current.Created = nil
	if current.CreatedAt == 0 {
		current.CreatedAt = time.Now().Unix()
	}
	dirExisted := report.Existing != nil || facts["xdial_state"] != ""
	fail := func(stepID string, cause error) (Result, error) {
		progress(Event{Step: stepID, Status: "failed", Message: AsError(cause).Message})
		if !s.rollback(context.WithoutCancel(ctx), undos, progress) {
			return Result{}, incompleteUndo()
		}
		if dirExisted {
			// The run is fully undone; the record goes back to what it was.
			if err := s.saveState(context.WithoutCancel(ctx), previous); err != nil {
				return Result{}, incompleteUndo()
			}
		}
		if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return Result{}, newError("cancelled", "已取消，本次改动已撤销", "Cancelled; this run's changes were undone")
		}
		return Result{}, cause
	}
	step := func(id, resource string, do func() error, undoFn func(context.Context) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress(Event{Step: id, Status: "running"})
		// Persist intent and register compensation before the first possible mutation.
		if resource != "" {
			current.Created = append(current.Created, resource)
			if err := s.saveState(ctx, current); err != nil {
				return err
			}
		}
		if undoFn != nil {
			undos = append(undos, undo{step: id, run: undoFn})
		}
		if err := do(); err != nil {
			return err
		}
		progress(Event{Step: id, Status: "done"})
		return nil
	}

	current.InProgress = true
	current.Created = nil
	if err := step("prepare", "", func() error {
		if err := s.mustPrivileged(ctx, "mkdir -p -m 700 "+configDir, "无法创建 "+configDir); err != nil {
			return err
		}
		if !dirExisted {
			current.Created = append(current.Created, "config_dir")
		}
		return s.saveState(ctx, current)
	}, func(c context.Context) error {
		if dirExisted {
			return nil
		}
		return s.mustPrivileged(c, "rm -rf "+configDir, "无法撤销目录")
	}); err != nil {
		return fail("prepare", err)
	}

	allowedProtos := ufwProtocols(plan.Protocol)
	keepRule := func(rule string) bool {
		for _, proto := range allowedProtos {
			if rule == strconv.Itoa(plan.Port)+"/"+proto {
				return true
			}
		}
		return false
	}

	for _, planned := range plan.Steps {
		var err error
		switch planned.ID {
		case "apt_key":
			err = step(planned.ID, "apt_key", func() error {
				if err := s.mustPrivileged(ctx, "mkdir -p -m 755 /etc/apt/keyrings", "无法创建 /etc/apt/keyrings"); err != nil {
					return err
				}
				return s.writeFile(ctx, aptKeyPath, "644", sagernetKey)
			}, s.removeFiles(aptKeyPath))
		case "apt_source":
			err = step(planned.ID, "apt_source", func() error {
				return s.writeFile(ctx, aptSourcePath, "644", []byte(aptSourceFile))
			}, s.removeFiles(aptSourcePath))
			current.AddedSource = err == nil
		case "install":
			source := plan.source
			if source == "" {
				source = aptSourcePath
			}
			err = step(planned.ID, "package", func() error { return s.installSingBox(ctx, source) }, func(c context.Context) error {
				return s.mustPrivileged(c, purgePackage, "无法撤销软件包安装")
			})
			current.InstalledPackage = err == nil || previous.InstalledPackage
		case "config":
			var config []byte
			var buildErr error
			if plan.Protocol == ProtocolVLESSReality {
				config, buildErr = serverConfigVLESSRealityJSON(plan.Port, plan.ServerName, userUUID, privateKey, shortID)
			} else {
				config, buildErr = serverConfigJSON(plan.Port, password)
			}
			if buildErr != nil {
				err = buildErr
				break
			}
			hadConfig := report.Existing != nil
			resource := "config"
			if hadConfig {
				resource = "config_moved"
			}
			if hadConfig {
				if err = s.mustPrivileged(ctx, "cp -p "+configPath+" "+configPath+".previous.tmp && mv -f "+configPath+".previous.tmp "+configPath+".previous", "无法备份原配置"); err != nil {
					break
				}
			}
			err = step(planned.ID, resource, func() error {
				if err := s.writeFile(ctx, configPath, "600", config); err != nil {
					return err
				}
				return s.checkConfig(ctx, plan.binary)
			}, func(c context.Context) error { return s.undoResource(c, resource, current) })
		case "unit":
			err = step(planned.ID, "unit", func() error {
				if err := s.writeFile(ctx, unitPath, "644", []byte(unitFile(plan.binary))); err != nil {
					return err
				}
				// Only creation owns boot enablement. Existing units retain the
				// administrator's boot policy, so rollback never has to guess it.
				return s.mustPrivileged(ctx, "systemctl daemon-reload && systemctl enable "+serviceName+" >/dev/null 2>&1", "无法启用新服务")
			}, func(c context.Context) error { return s.undoResource(c, "unit", current) })
		case "ufw":
			for _, proto := range allowedProtos {
				rule := strconv.Itoa(plan.Port) + "/" + proto
				var exists bool
				exists, err = s.hasUFWRule(ctx, rule)
				if err != nil {
					break
				}
				if exists {
					continue
				}
				resource := "ufw_rule:" + rule
				err = step(planned.ID, resource, func() error {
					return s.mustPrivileged(ctx, "ufw allow "+rule+" comment "+serviceName, "无法添加 ufw 放行规则")
				}, func(c context.Context) error { return s.undoResource(c, resource, current) })
				if err != nil {
					break
				}
				current.OwnedUFW = append(current.OwnedUFW, rule)
			}

		case "start":
			err = step(planned.ID, "service", func() error { return s.startService(ctx, plan.Port) }, func(c context.Context) error {
				return s.undoResource(c, "service", current)
			})
		}
		if err != nil {
			return fail(planned.ID, err)
		}
	}
	// Commit the durable result before deleting the rollback backup. Keep existing
	// firewall rules: only resources with recorded ownership may ever be removed.
	committed := current
	// Relinquish retired rules durably before best-effort cleanup. Once retired,
	// an administrator can reuse that port without a later run deleting its rule.
	committed.OwnedUFW = nil
	for _, rule := range current.OwnedUFW {
		if keepRule(rule) {
			committed.OwnedUFW = append(committed.OwnedUFW, rule)
		}
	}
	committed.InProgress = false
	committed.Created = nil
	committed.Baseline = nil
	if err := s.saveState(ctx, committed); err != nil {
		return fail("commit", err)
	}
	for _, rule := range previous.OwnedUFW {
		if !keepRule(rule) {
			// Cleanup follows commit so a failed deployment never loses its old access.
			_ = s.undoResource(ctx, "ufw_rule:"+rule, previous)
		}
	}
	_, _ = s.runPrivileged(ctx, "rm -f "+configPath+".previous", nil)
	serverHost := strings.Trim(strings.TrimSpace(s.target.Host), "[]")
	if plan.Protocol == ProtocolVLESSReality {
		return Result{
			Protocol:    ProtocolVLESSReality,
			Server:      serverHost,
			Port:        plan.Port,
			Method:      plan.Method,
			UUID:        userUUID,
			Flow:        flow,
			ServerName:  plan.ServerName,
			PublicKey:   publicKey,
			ShortID:     shortID,
			Fingerprint: vlessDefaultFingerprint,
			Reused:      plan.Reuse,
		}, nil
	}
	return Result{
		Protocol: ProtocolShadowsocks,
		Server:   serverHost,
		Port:     plan.Port,
		Method:   plan.Method,
		Password: password,
		Reused:   plan.Reuse,
	}, nil
}

// rollback undoes this run in reverse order and reports whether every step
// was undone. It cannot help if the connection is gone; the in-progress
// record on the server then lets the next run finish the job.
func (s *Session) rollback(ctx context.Context, undos []undo, progress func(Event)) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	complete := true
	for i := len(undos) - 1; i >= 0; i-- {
		progress(Event{Step: undos[i].step, Status: "rolling_back"})
		if err := undos[i].run(ctx); err != nil {
			complete = false
			progress(Event{Step: undos[i].step, Status: "failed", Message: "这一步没能撤销"})
			break
		}
		progress(Event{Step: undos[i].step, Status: "rolled_back"})
	}
	return complete
}

func incompleteUndo() error {
	return newError("undo_incomplete",
		"与服务器的连接中断，本次的改动没能全部撤销。服务器上记下了这些改动；下次连接这台服务器设置时，会先撤销它们再继续。",
		"The connection to the server was lost, so this run's changes could not all be undone. The server keeps a record of them; the next setup on this server undoes them first.")
}

const purgePackage = "export DEBIAN_FRONTEND=noninteractive; dpkg --configure -a >/dev/null 2>&1; apt-get purge -y -o DPkg::Lock::Timeout=120 sing-box >/dev/null 2>&1; ! dpkg -s sing-box >/dev/null 2>&1"

// recoverInterrupted undoes, newest first, what an interrupted run recorded,
// and returns the record as it stands afterwards.
func (s *Session) recoverInterrupted(ctx context.Context, interrupted state) (state, error) {
	dirRemoved := interrupted.created("config_dir")
	for len(interrupted.Created) > 0 {
		i := len(interrupted.Created) - 1
		resource := interrupted.Created[i]
		if err := s.undoResource(ctx, resource, interrupted); err != nil {
			return interrupted, err
		}
		interrupted.Created = interrupted.Created[:i]
		if resource != "config_dir" {
			if err := s.saveState(ctx, interrupted); err != nil {
				return interrupted, err
			}
		}
	}
	recovered := state{Version: 1}
	if interrupted.Baseline != nil {
		recovered = *interrupted.Baseline
	}
	// A fresh run removed its directory; a move retains the original record.
	if !dirRemoved {
		if err := s.saveState(ctx, recovered); err != nil {
			return interrupted, err
		}
	}
	return recovered, nil
}

// undoResource is the shared live-rollback and crash-recovery path. Each action
// is repeatable until its journal entry has been durably removed.
func (s *Session) undoResource(ctx context.Context, resource string, st state) error {
	var script string
	switch {
	case resource == "service":
		if st.Baseline != nil && !st.created("unit") {
			if st.WasActive {
				return nil
			}
			script = "systemctl stop " + serviceName
		} else {
			script = "if systemctl cat " + serviceName + " >/dev/null 2>&1; then systemctl disable --now " + serviceName + "; fi"
		}
	case resource == "unit":
		script = "if [ -f " + unitPath + " ]; then systemctl disable --now " + serviceName + " && rm -f " + unitPath + "; fi && systemctl daemon-reload"
	case resource == "config":
		script = "rm -f " + configPath
	case resource == "config_moved":
		script = "cp -p " + configPath + ".previous " + configPath
		if st.WasActive {
			script += " && systemctl restart " + serviceName
		}
	case resource == "package":
		script = purgePackage
	case resource == "apt_source":
		script = "rm -f " + aptSourcePath
	case resource == "apt_key":
		script = "rm -f " + aptKeyPath
	case resource == "config_dir":
		script = "rm -rf " + configDir
	case strings.HasPrefix(resource, "ufw_rule:"):
		rule := strings.TrimPrefix(resource, "ufw_rule:")
		parts := strings.Split(rule, "/")
		if len(parts) != 2 || (parts[1] != "tcp" && parts[1] != "udp") {
			return incompleteUndo()
		}
		port, err := strconv.Atoi(parts[0])
		if err != nil || (port != 443 && (port < 1024 || port > 65535)) {
			return incompleteUndo()
		}
		exists, err := s.hasUFWRule(ctx, rule)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		script = "ufw delete allow " + rule
	default:
		return incompleteUndo() // Never guess ownership for legacy entries.
	}
	return s.mustPrivileged(ctx, script, "无法撤销服务器改动")
}

func (s *Session) hasUFWRule(ctx context.Context, rule string) (bool, error) {
	result, err := s.runPrivileged(ctx, "ufw show added", nil)
	if err != nil {
		return false, err
	}
	if result.code != 0 {
		return false, incompleteUndo()
	}
	for _, line := range strings.Split(string(result.stdout), "\n") {
		line = strings.TrimSpace(line)
		want := "ufw allow " + rule
		if line == want || strings.HasPrefix(line, want+" comment ") {
			return true, nil
		}
	}
	return false, nil
}

func (s *Session) saveState(ctx context.Context, st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.writeFile(ctx, statePath, "600", raw)
}

func previousState(f facts) state {
	var previous state
	if encoded := f["xdial_state"]; encoded != "" {
		if raw, err := base64.StdEncoding.DecodeString(encoded); err == nil {
			_ = json.Unmarshal(raw, &previous)
		}
	}
	return previous
}

func (s *Session) mustPrivileged(ctx context.Context, script, message string) error {
	result, err := s.runPrivileged(ctx, script, nil)
	if err != nil {
		return err
	}
	if result.code != 0 {
		return newError("command_failed", message, "A command failed on the server")
	}
	return nil
}

// writeFile replaces path atomically through stdin, so the content never
// appears on a command line.
func (s *Session) writeFile(ctx context.Context, path, mode string, content []byte) error {
	tmp := path + ".xdial-tmp"
	script := "umask 077 && cat > " + tmp + " && chmod " + mode + " " + tmp + " && mv -f " + tmp + " " + path
	result, err := s.runPrivileged(ctx, script, content)
	if err != nil {
		return err
	}
	if result.code != 0 {
		_, _ = s.runPrivileged(context.WithoutCancel(ctx), "rm -f "+tmp, nil)
		return newError("write_failed", "无法写入 "+path, "Could not write "+path)
	}
	return nil
}

func (s *Session) removeFiles(paths ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		return s.mustPrivileged(ctx, "rm -f "+strings.Join(paths, " "), "无法撤销文件")
	}
}

const aptSourceFile = `Types: deb
URIs: https://deb.sagernet.org/
Suites: *
Components: *
Enabled: yes
Signed-By: ` + aptKeyPath + `
`

// installSingBox refreshes only the sing-box repository and installs the
// package; apt checks every package against the repository signing key.
func (s *Session) installSingBox(ctx context.Context, source string) error {
	if strings.ContainsAny(source, " '\"`$;&|<>\n") {
		return newError("bad_source", "服务器上的 sing-box 软件源路径无法使用", "The sing-box repository path on the server cannot be used")
	}
	sourceOptions := "-o Dir::Etc::sourcelist=" + source + " -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0"
	if strings.HasSuffix(source, "/sources.list") {
		sourceOptions = ""
	}
	script := "log=$(mktemp) || exit 1; trap 'rm -f \"$log\"' EXIT; export DEBIAN_FRONTEND=noninteractive; " +
		"apt-get update -o DPkg::Lock::Timeout=120 " + sourceOptions + " >\"$log\" 2>&1 && " +
		"apt-get install -y --no-install-recommends -o DPkg::Lock::Timeout=120 sing-box >>\"$log\" 2>&1 && " +
		"test -x " + defaultBinary + " && " + defaultBinary + " version >/dev/null; status=$?; " +
		"if [ $status -ne 0 ]; then tail -n 5 \"$log\" >&2; fi; rm -f \"$log\"; exit $status"
	result, err := s.runPrivileged(ctx, script, nil)
	if err != nil {
		return err
	}
	if result.code != 0 {
		detail := aptFailureReason(string(result.stderr))
		return newError("install_failed", "安装 sing-box 失败："+detail.zh, "Installing sing-box failed: "+detail.en)
	}
	return nil
}

type reason struct{ zh, en string }

func aptFailureReason(stderr string) reason {
	switch {
	case strings.Contains(stderr, "NO_PUBKEY") || strings.Contains(stderr, "not signed") || strings.Contains(stderr, "BADSIG"):
		return reason{"软件源签名校验没有通过，已停止。", "the repository signature did not verify, so setup stopped."}
	case strings.Contains(stderr, "Could not resolve") || strings.Contains(stderr, "Failed to fetch") || strings.Contains(stderr, "Connection timed out"):
		return reason{"服务器无法访问 deb.sagernet.org。", "the server could not reach deb.sagernet.org."}
	case strings.Contains(stderr, "lock"):
		return reason{"系统正在安装其他软件（apt 被占用），请稍后重试。", "another package installation is running (apt is locked); try again later."}
	default:
		return reason{"apt 没有完成安装。", "apt did not complete the installation."}
	}
}

func (s *Session) checkConfig(ctx context.Context, binary string) error {
	result, err := s.runPrivileged(ctx, shellQuote(binary)+" check -c "+configPath, nil)
	if err != nil {
		return err
	}
	if result.code != 0 {
		return newError("config_rejected", "sing-box 不接受生成的配置，可能是版本过旧。", "sing-box rejected the configuration; its version may be too old.")
	}
	return nil
}

func (s *Session) startService(ctx context.Context, port int) error {
	p := strconv.Itoa(port)
	// The port must be held by this service's own process, and the service must
	// stay up: another program on the port would otherwise look like success.
	owned := "pid=$(systemctl show -p MainPID --value " + serviceName + "); [ \"${pid:-0}\" -gt 0 ] && " +
		"ss -Hltnp 'sport = :" + p + "' | grep -q \"pid=$pid,\""
	script := "systemctl daemon-reload && systemctl restart " + serviceName + " && " +
		"for i in 1 2 3 4 5 6 7 8 9 10; do " +
		"if systemctl is-active --quiet " + serviceName + " && " + owned + "; then " +
		"sleep 2; restarts=$(systemctl show -p NRestarts --value " + serviceName + "); " +
		"if systemctl is-active --quiet " + serviceName + " && [ \"${restarts:-0}\" = 0 ] && " + owned + "; then exit 0; fi; fi; " +
		"sleep 1; done; exit 1"
	result, err := s.runPrivileged(ctx, script, nil)
	if err != nil {
		return err
	}
	if result.code != 0 {
		return newError("service_not_listening", "服务没有在端口 "+p+" 上正常监听。", "The service did not start listening on port "+p+".")
	}
	return nil
}

func serverConfigJSON(port int, password string) ([]byte, error) {
	document := map[string]interface{}{
		"log": map[string]interface{}{"level": "warn"},
		"inbounds": []interface{}{map[string]interface{}{
			"type":        "shadowsocks",
			"tag":         inboundTag,
			"listen":      "::",
			"listen_port": port,
			"method":      shadowsocksKey,
			"password":    password,
		}},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(document, "", "  ")
}

func serverConfigVLESSRealityJSON(port int, serverName, userUUID, privateKey, shortID string) ([]byte, error) {
	document := map[string]interface{}{
		"log": map[string]interface{}{"level": "warn"},
		"inbounds": []interface{}{map[string]interface{}{
			"type":        "vless",
			"tag":         vlessInboundTag,
			"listen":      "::",
			"listen_port": port,
			"users": []interface{}{map[string]interface{}{
				"name": "xdial",
				"uuid": userUUID,
				"flow": vlessVisionFlow,
			}},
			"tls": map[string]interface{}{
				"enabled":     true,
				"server_name": serverName,
				"reality": map[string]interface{}{
					"enabled": true,
					"handshake": map[string]interface{}{
						"server":      serverName,
						"server_port": 443,
					},
					"private_key": privateKey,
					"short_id":    []string{shortID},
				},
			},
		}},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(document, "", "  ")
}

func unitFile(binary string) string {
	return fmt.Sprintf(`[Unit]
Description=XDial proxy (sing-box), installed by XDial
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run -c %s
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, binary, configPath)
}

// newShadowsocksKey returns a random 16-byte key, as 2022-blake3-aes-128-gcm requires.
func newShadowsocksKey() (string, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// newVLESSRealityMaterial generates a UUIDv4, an X25519 Reality keypair and an 8-byte hex short_id.
func newVLESSRealityMaterial() (userUUID, privateKey, publicKey, shortID string, err error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", "", "", "", err
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", "", err
	}
	sidBytes := make([]byte, 8)
	if _, err := rand.Read(sidBytes); err != nil {
		return "", "", "", "", err
	}
	return id.String(),
		base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		hex.EncodeToString(sidBytes),
		nil
}
