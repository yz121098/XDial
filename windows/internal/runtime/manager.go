package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kafeifei/xdial/core/config"
	"github.com/kafeifei/xdial/core/libbox"
	"github.com/kafeifei/xdial/core/subscription"
	"github.com/kafeifei/xdial/windows/internal/netwin"
	"github.com/kafeifei/xdial/windows/internal/store"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
)

type Phase string

const (
	PhaseDisconnected  Phase = "disconnected"
	PhaseConnecting    Phase = "connecting"
	PhaseConnected     Phase = "connected"
	PhaseSwitching     Phase = "switching"
	PhaseDisconnecting Phase = "disconnecting"
	PhaseFailed        Phase = "failed"
)

type TaskState string

const (
	TaskPending     TaskState = "pending"
	TaskRunning     TaskState = "running"
	TaskReady       TaskState = "ready"
	TaskCommitting  TaskState = "committing"
	TaskCommitted   TaskState = "committed"
	TaskRollingBack TaskState = "rolling_back"
	TaskRolledBack  TaskState = "rolled_back"
	TaskFailed      TaskState = "failed"
	TaskSkipped     TaskState = "skipped"
)

type TaskProgress struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	Detail string    `json:"detail,omitempty"`
	State  TaskState `json:"state"`
}

type ActiveLineStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	ExitIPv4  string `json:"exit_ipv4,omitempty"`
	ExitIPv6  string `json:"exit_ipv6,omitempty"`
	LatencyMS *int   `json:"latency_ms,omitempty"`
	Testing   bool   `json:"testing,omitempty"`
	Error     string `json:"error,omitempty"`
}

type RouteMappingRow struct {
	RuleID    string `json:"rule_id,omitempty"`
	RuleName  string `json:"rule_name"`
	LineID    string `json:"line_id"`
	LineName  string `json:"line_name"`
	LatencyMS *int   `json:"latency_ms,omitempty"`
	IsDefault bool   `json:"is_default,omitempty"`
}

type SwitchFailureInfo struct {
	FromScenario string `json:"from_scenario"`
	ToScenario   string `json:"to_scenario"`
	Message      string `json:"message"`
}

type FailureDetails struct {
	Title        string `json:"title"`
	RawError     string `json:"raw_error"`
	IngressFact  string `json:"ingress_fact"`
	RollbackFact string `json:"rollback_fact"`
	RollbackOK   bool   `json:"rollback_ok"`
}

type LatencyRecord struct {
	Milliseconds *int   `json:"milliseconds,omitempty"`
	Error        string `json:"error,omitempty"`
	Testing      bool   `json:"testing,omitempty"`
	ObservedAt   int64  `json:"observed_at,omitempty"`
}

type ProfileSourceItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IsSubscription bool   `json:"is_subscription"`
	RemoteURL      string `json:"remote_url,omitempty"`
}

type EditorView struct {
	EditingSourceID   string              `json:"editing_source_id"`
	EditingSourceName string              `json:"editing_source_name"`
	Sources           []ProfileSourceItem `json:"sources"`
	Lines             []config.Line       `json:"lines"`
	LineGroups        []config.Line       `json:"line_groups"`
	RuleSets          []config.RuleSet    `json:"rule_sets"`
	Scenarios         []config.Scenario   `json:"scenarios"`
	ActiveScenarioID  string              `json:"active_scenario_id"`
	AllLines          []config.Line       `json:"all_lines"`
	AllRuleSets       []config.RuleSet    `json:"all_rule_sets"`
}

type AppSnapshot struct {
	Phase              Phase                    `json:"phase"`
	TargetScenarioName string                   `json:"target_scenario_name,omitempty"`
	ActiveIngressMode  store.IngressMode        `json:"active_ingress_mode,omitempty"`
	ActiveSOCKSPort    int                      `json:"active_socks_port,omitempty"`
	IsProxyOnly        bool                     `json:"is_proxy_only"`
	ConnectedAt        *int64                   `json:"connected_at,omitempty"`
	DownloadBps        *float64                 `json:"download_bps,omitempty"`
	UploadBps          *float64                 `json:"upload_bps,omitempty"`
	ConfigDirty        bool                     `json:"config_dirty"`
	NeedsSignIn        bool                     `json:"needs_sign_in"`
	FreshUnconfigured  bool                     `json:"fresh_unconfigured"`
	BlockerMessage     string                   `json:"blocker_message,omitempty"`
	AutoReason         string                   `json:"auto_reason,omitempty"`
	ParkedRecovery     bool                     `json:"parked_recovery"`
	SwitchFailure      *SwitchFailureInfo       `json:"switch_failure,omitempty"`
	Failure            *FailureDetails          `json:"failure,omitempty"`
	Tasks              []TaskProgress           `json:"tasks,omitempty"`
	ActiveLines        []ActiveLineStatus       `json:"active_lines,omitempty"`
	RouteMappings      []RouteMappingRow        `json:"route_mappings,omitempty"`
	Editor             EditorView               `json:"editor"`
	Latencies          map[string]LatencyRecord `json:"latencies"`
	Preferences        store.Preferences        `json:"preferences"`
	SystemFacts        netwin.UnderlaySnapshot  `json:"system_facts"`
	Version            string                   `json:"version"`
}

type boxCallback struct {
	mgr *Manager
}

func (c *boxCallback) OnStatusChanged(statusJSON string) {}

func (c *boxCallback) OnError(code int, message string) {
	c.mgr.handleRuntimeError(code, message)
}

type Manager struct {
	mu                     sync.Mutex
	store                  *store.Store
	version                string
	onUpdate               func(AppSnapshot)
	onCloudHostEvent       func(map[string]any)

	libraryJSON            string
	prefs                  store.Preferences
	systemFacts            netwin.UnderlaySnapshot

	phase                  Phase
	targetScenarioName     string
	activeBox              *libbox.Libbox
	activeScenarioID       string
	activeScenarioName     string
	activeFingerprint      string
	activeIngressMode      store.IngressMode
	activeSOCKSPort        int
	activeLineOutbounds    map[string]string
	activeLines            []ActiveLineStatus
	connectedAt            *int64
	downloadBps            *float64
	uploadBps              *float64
	lastUploadBytes        int64
	lastDownloadBytes      int64
	hasBaselineSample      bool

	tasks                  []TaskProgress
	failure                *FailureDetails
	switchFailure          *SwitchFailureInfo
	parkedRecovery         bool
	autoReason             string

	connectCancel          context.CancelFunc
	latencyCancel          context.CancelFunc
	latencies              map[string]LatencyRecord
	cloudHost              *libbox.CloudHostSetup

	stopCh                 chan struct{}
}

func NewManager(st *store.Store, version string, onUpdate func(AppSnapshot), onCloudHostEvent func(map[string]any)) (*Manager, error) {
	libJSON, err := st.LoadLibrary()
	if err != nil {
		return nil, err
	}
	prefs := st.LoadPreferences()
	// Clean up any leftover WinINET proxy settings from a previous unclean exit
	_ = netwin.DisableSystemProxy(st.RootDir())

	facts, _ := netwin.SnapshotUnderlay()
	if facts == nil {
		facts = &netwin.UnderlaySnapshot{
			DNSServers:   []string{"223.5.5.5", "1.1.1.1"},
			IsElevated:   netwin.IsProcessElevated(),
			HasWintunDLL: netwin.HasWintunDLL(),
		}
	}

	m := &Manager{
		store:            st,
		version:          version,
		onUpdate:         onUpdate,
		onCloudHostEvent: onCloudHostEvent,
		libraryJSON:      libJSON,
		prefs:            prefs,
		systemFacts:      *facts,
		phase:            PhaseDisconnected,
		latencies:        make(map[string]LatencyRecord),
		cloudHost:        libbox.NewCloudHostSetup(),
		stopCh:           make(chan struct{}),
	}
	m.repairImportedMobileLibraryLocked()
	if prefs.AutomaticScenario {
		m.evaluateAutoScenarioLocked()
	}
	go m.backgroundTicker()
	return m, nil
}

func (m *Manager) Shutdown() {
	close(m.stopCh)
	_ = m.Disconnect()
	m.cloudHost.Close()
}

func (m *Manager) backgroundTicker() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	ssidTick := 0
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			ssidTick++
			m.mu.Lock()
			changed := false
			if m.phase == PhaseConnected && m.activeBox != nil {
				raw := m.activeBox.TrafficTotals()
				var totals struct {
					UploadBytes   int64 `json:"upload_bytes"`
					DownloadBytes int64 `json:"download_bytes"`
				}
				if json.Unmarshal([]byte(raw), &totals) == nil {
					if m.hasBaselineSample {
						dl := float64(totals.DownloadBytes - m.lastDownloadBytes)
						ul := float64(totals.UploadBytes - m.lastUploadBytes)
						if dl < 0 {
							dl = 0
						}
						if ul < 0 {
							ul = 0
						}
						m.downloadBps = &dl
						m.uploadBps = &ul
					}
					m.lastDownloadBytes = totals.DownloadBytes
					m.lastUploadBytes = totals.UploadBytes
					m.hasBaselineSample = true
					changed = true
				}
			}
			// Periodically refresh SSID & underlay facts every 6 seconds
			if ssidTick%6 == 0 {
				m.mu.Unlock()
				freshFacts, _ := netwin.SnapshotUnderlay()
				m.mu.Lock()
				if freshFacts != nil {
					prevSSID := m.systemFacts.ConnectedSSID
					m.systemFacts = *freshFacts
					if m.prefs.AutomaticScenario && freshFacts.ConnectedSSID != prevSSID {
						if m.evaluateAutoScenarioLocked() {
							changed = true
						}
					}
				}
			}
			var snap AppSnapshot
			if changed && m.onUpdate != nil {
				snap = m.buildSnapshotLocked()
			}
			cb := m.onUpdate
			m.mu.Unlock()
			if changed && cb != nil {
				cb(snap)
			}
		}
	}
}

func (m *Manager) evaluateAutoScenarioLocked() bool {
	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		return false
	}
	prof, err := config.ParseRuntimeProfile([]byte(rtJSON))
	if err != nil {
		return false
	}
	ssid := m.systemFacts.ConnectedSSID
	if ssid == "" {
		m.autoReason = "当前未识别到 Wi-Fi 名称，沿用所选场景"
		return false
	}
	for _, sc := range prof.Scenarios {
		for _, match := range sc.MatchSSIDs {
			if match == ssid {
				m.autoReason = fmt.Sprintf("已连上 Wi-Fi「%s」", ssid)
				if prof.ActiveScenarioID != sc.ID {
					_ = m.applyActiveScenarioIDLocked(sc.ID)
					return true
				}
				return false
			}
		}
	}
	m.autoReason = fmt.Sprintf("Wi-Fi「%s」未绑定专属场景，沿用所选场景", ssid)
	return false
}

func (m *Manager) applyActiveScenarioIDLocked(scenarioID string) error {
	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rtJSON), &doc); err != nil {
		return err
	}
	doc["active_scenario_id"], _ = json.Marshal(scenarioID)
	draftBytes, _ := json.Marshal(doc)
	nextLib, err := libbox.ApplyProfileLibrarySelection(m.libraryJSON, string(draftBytes))
	if err != nil {
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		return err
	}
	m.libraryJSON = nextLib
	return nil
}

func (m *Manager) emitUpdate() {
	m.mu.Lock()
	snap := m.buildSnapshotLocked()
	cb := m.onUpdate
	m.mu.Unlock()
	if cb != nil {
		cb(snap)
	}
}

func (m *Manager) GetSnapshot() AppSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buildSnapshotLocked()
}

func (m *Manager) buildEditorViewLocked() (*config.Profile, EditorView) {
	var view EditorView
	view.Lines = []config.Line{}
	view.LineGroups = []config.Line{}
	view.RuleSets = []config.RuleSet{}
	view.Scenarios = []config.Scenario{}
	view.Sources = []ProfileSourceItem{}

	// Decode library structure to extract per-source resources cleanly
	var rawLib struct {
		EditingProfileID string `json:"editingProfileID"`
		Profiles         []struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Source  json.RawMessage `json:"source,omitempty"`
			Profile struct {
				Lines    []config.Line    `json:"lines"`
				RuleSets []config.RuleSet `json:"rule_sets"`
			} `json:"profile"`
		} `json:"profiles"`
	}
	_ = json.Unmarshal([]byte(m.libraryJSON), &rawLib)
	view.EditingSourceID = rawLib.EditingProfileID

	for _, rec := range rawLib.Profiles {
		var remote struct {
			URL string `json:"url"`
		}
		if len(rec.Source) > 0 {
			_ = json.Unmarshal(rec.Source, &remote)
		}
		view.Sources = append(view.Sources, ProfileSourceItem{
			ID:             rec.ID,
			Name:           rec.Name,
			IsSubscription: remote.URL != "",
			RemoteURL:      remote.URL,
		})
		if rec.ID == rawLib.EditingProfileID {
			view.EditingSourceName = rec.Name
			hasDirect := false
			for _, l := range rec.Profile.Lines {
				if l.ID == "direct" {
					hasDirect = true
				}
			}
			if !hasDirect {
				view.Lines = append(view.Lines, config.Line{
					ID: "direct", Name: "直连", Type: config.LineTypeDirect, Enabled: true,
				})
			}
			view.Lines = append(view.Lines, rec.Profile.Lines...)
			view.RuleSets = append(view.RuleSets, rec.Profile.RuleSets...)
		}
	}

	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		return nil, view
	}
	prof, err := config.ParseRuntimeProfile([]byte(rtJSON))
	if err != nil {
		return nil, view
	}
	view.ActiveScenarioID = prof.ActiveScenarioID
	view.Scenarios = prof.Scenarios
	view.AllRuleSets = prof.RuleSets
	for _, l := range prof.Lines {
		if l.IsGroup() {
			view.LineGroups = append(view.LineGroups, l)
		}
		view.AllLines = append(view.AllLines, l)
	}
	return prof, view
}

func (m *Manager) buildSnapshotLocked() AppSnapshot {
	prof, editor := m.buildEditorViewLocked()

	configDirty := false
	needsSignIn := false
	freshUnconfigured := false
	blocker := ""
	var routeMappings []RouteMappingRow

	if prof != nil {
		nonDirectCount := 0
		for _, l := range prof.Lines {
			if l.ID != "direct" {
				nonDirectCount++
			}
		}
		if nonDirectCount == 0 && !m.prefs.OnboardingDismissed {
			freshUnconfigured = true
		}

		// Mirror iOS HomeView.activeScenarioNeedsSignIn: if the active scenario
		// uses an AnyConnect VPN line whose credentials or server are incomplete,
		// surface NeedsSignIn instead of a generic configuration error rail.
		if vpnLine := prof.ActiveVPNLine(); vpnLine != nil {
			if strings.TrimSpace(vpnLine.VPNServer) == "" ||
				strings.TrimSpace(vpnLine.VPNUsername) == "" ||
				vpnLine.VPNPassword == "" {
				needsSignIn = true
			}
		} else {
			for _, l := range prof.Lines {
				if l.Type == config.LineTypeVPN && l.Enabled {
					if strings.TrimSpace(l.VPNUsername) == "" || l.VPNPassword == "" {
						if !m.prefs.OnboardingDismissed {
							needsSignIn = true
						}
					}
					break
				}
			}
		}

		plan, err := config.BuildConnectionPlan(prof)
		if err != nil {
			if !needsSignIn {
				blocker = err.Error()
			}
		} else if m.phase == PhaseConnected {
			if plan.ConfigurationFingerprint != m.activeFingerprint ||
				m.prefs.IngressMode != m.activeIngressMode ||
				m.prefs.SOCKSPort != m.activeSOCKSPort {
				configDirty = true
			}
		}

		if sc := prof.ActiveScenario(); sc != nil {
			lineNameOf := func(id string) string {
				if id == "direct" || id == "" {
					return "直连"
				}
				if l := prof.FindLine(id); l != nil {
					return l.Name
				}
				return id
			}
			for _, b := range sc.Bindings {
				rs := prof.FindRuleSet(b.RuleSetID)
				if rs == nil || !rs.Enabled {
					continue
				}
				var lat *int
				if rec, ok := m.latencies[b.LineID]; ok {
					lat = rec.Milliseconds
				}
				routeMappings = append(routeMappings, RouteMappingRow{
					RuleID:    b.RuleSetID,
					RuleName:  rs.Name,
					LineID:    b.LineID,
					LineName:  lineNameOf(b.LineID),
					LatencyMS: lat,
				})
			}
			defLine := sc.DefaultLineID
			if defLine == "" {
				defLine = "direct"
			}
			var defLat *int
			if rec, ok := m.latencies[defLine]; ok {
				defLat = rec.Milliseconds
			}
			routeMappings = append(routeMappings, RouteMappingRow{
				RuleName:  "其他流量",
				LineID:    defLine,
				LineName:  lineNameOf(defLine),
				LatencyMS: defLat,
				IsDefault: true,
			})
		}
	}

	latCopy := make(map[string]LatencyRecord, len(m.latencies))
	for k, v := range m.latencies {
		latCopy[k] = v
	}

	activeLines := make([]ActiveLineStatus, len(m.activeLines))
	copy(activeLines, m.activeLines)
	for i := range activeLines {
		if rec, ok := latCopy[activeLines[i].ID]; ok {
			activeLines[i].LatencyMS = rec.Milliseconds
			activeLines[i].Testing = rec.Testing
			if activeLines[i].Error == "" {
				activeLines[i].Error = rec.Error
			}
		}
	}

	tasksCopy := make([]TaskProgress, len(m.tasks))
	copy(tasksCopy, m.tasks)

	return AppSnapshot{
		Phase:              m.phase,
		TargetScenarioName: m.targetScenarioName,
		ActiveIngressMode:  m.activeIngressMode,
		ActiveSOCKSPort:    m.activeSOCKSPort,
		IsProxyOnly:        m.phase == PhaseConnected && m.activeIngressMode == store.IngressLocalSOCKS,
		ConnectedAt:        m.connectedAt,
		DownloadBps:        m.downloadBps,
		UploadBps:          m.uploadBps,
		ConfigDirty:        configDirty,
		NeedsSignIn:        needsSignIn,
		FreshUnconfigured:  freshUnconfigured,
		BlockerMessage:     blocker,
		AutoReason:         m.autoReason,
		ParkedRecovery:     m.parkedRecovery,
		SwitchFailure:      m.switchFailure,
		Failure:            m.failure,
		Tasks:              tasksCopy,
		ActiveLines:        activeLines,
		RouteMappings:      routeMappings,
		Editor:             editor,
		Latencies:          latCopy,
		Preferences:        m.prefs,
		SystemFacts:        m.systemFacts,
		Version:            m.version,
	}
}

func (m *Manager) handleRuntimeError(code int, message string) {
	m.mu.Lock()
	if m.phase == PhaseConnected {
		_ = netwin.DisableSystemProxy(m.store.RootDir())
		m.phase = PhaseFailed
		m.connectedAt = nil
		m.downloadBps = nil
		m.uploadBps = nil
		m.failure = &FailureDetails{
			Title:        "运行中连接中断",
			RawError:     message,
			IngressFact:  "系统代理已自动撤销",
			RollbackFact: "已回滚，原网络可用",
			RollbackOK:   true,
		}
	}
	snap := m.buildSnapshotLocked()
	cb := m.onUpdate
	m.mu.Unlock()
	if cb != nil {
		cb(snap)
	}
}

// adaptConfigForWindows rewrites the generated PacketTunnel/Android sing-box JSON
// to bind the requested Windows ingress (Mixed HTTP+SOCKS5 loopback and/or Wintun)
// and pins route.default_interface to the pre-start Windows default interface.
func adaptConfigForWindows(rawConfig []byte, mode store.IngressMode, alsoSOCKS bool, socksPort int, defaultIface string) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(rawConfig, &doc); err != nil {
		return "", fmt.Errorf("parse compiled sing-box config: %w", err)
	}

	var inbounds []any
	switch mode {
	case store.IngressWintun:
		inbounds = append(inbounds, map[string]any{
			"type":           "tun",
			"tag":            "windows-tun-in",
			"interface_name": "XDial",
			"address":        []string{"172.19.0.1/30"},
			"mtu":            9000,
			"auto_route":     true,
			"strict_route":   true,
			"stack":          "gvisor",
		})
		if alsoSOCKS {
			inbounds = append(inbounds, map[string]any{
				"type":        "mixed",
				"tag":         "windows-mixed-in",
				"listen":      "127.0.0.1",
				"listen_port": socksPort,
			})
		}
	default: // IngressSystemProxy or IngressLocalSOCKS
		inbounds = append(inbounds, map[string]any{
			"type":        "mixed",
			"tag":         "windows-mixed-in",
			"listen":      "127.0.0.1",
			"listen_port": socksPort,
		})
	}
	doc["inbounds"] = inbounds

	if routeMap, ok := doc["route"].(map[string]any); ok && defaultIface != "" {
		routeMap["default_interface"] = defaultIface
		delete(routeMap, "auto_detect_interface")
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (m *Manager) setTaskStateLocked(taskID string, state TaskState) {
	for i := range m.tasks {
		if m.tasks[i].ID == taskID {
			m.tasks[i].State = state
			return
		}
	}
}

func (m *Manager) Connect() error {
	return m.startTransaction(false)
}

func (m *Manager) Reconnect() error {
	return m.startTransaction(true)
}

func (m *Manager) startTransaction(isSwitch bool) error {
	m.mu.Lock()
	if m.phase == PhaseConnecting || m.phase == PhaseSwitching || m.phase == PhaseDisconnecting {
		m.mu.Unlock()
		return fmt.Errorf("连接事务正在进行中")
	}

	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	prof, err := config.ParseRuntimeProfile([]byte(rtJSON))
	if err != nil {
		m.mu.Unlock()
		return err
	}
	plan, err := config.BuildConnectionPlan(prof)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	prevBox := m.activeBox
	prevScenarioName := m.activeScenarioName
	wasConnected := m.phase == PhaseConnected && prevBox != nil

	ctx, cancel := context.WithCancel(context.Background())
	m.connectCancel = cancel
	m.failure = nil
	m.switchFailure = nil
	m.parkedRecovery = false

	tasks := make([]TaskProgress, 0, len(plan.Tasks))
	for _, t := range plan.Tasks {
		tasks = append(tasks, TaskProgress{
			ID:     t.ID,
			Kind:   t.Kind,
			Name:   t.Name,
			Detail: t.Detail,
			State:  TaskPending,
		})
	}
	m.tasks = tasks
	m.targetScenarioName = plan.Scenario.Name
	if wasConnected && isSwitch {
		m.phase = PhaseSwitching
	} else {
		m.phase = PhaseConnecting
	}
	desiredMode := m.prefs.IngressMode
	desiredPort := m.prefs.SOCKSPort
	alsoSOCKS := m.prefs.AlsoExposeLocalSOCKS
	m.mu.Unlock()
	m.emitUpdate()

	go m.executeTransaction(ctx, rtJSON, prof, plan, desiredMode, desiredPort, alsoSOCKS, wasConnected, prevBox, prevScenarioName)
	return nil
}

func (m *Manager) executeTransaction(
	ctx context.Context,
	rtJSON string,
	prof *config.Profile,
	plan *config.ConnectionPlan,
	desiredMode store.IngressMode,
	desiredPort int,
	alsoSOCKS bool,
	wasConnected bool,
	prevBox *libbox.Libbox,
	prevScenarioName string,
) {
	failTx := func(taskName, rawErr string, takeoverSubmitted bool) {
		if ctx.Err() != nil {
			m.mu.Lock()
			if wasConnected && prevBox != nil {
				m.phase = PhaseConnected
				m.targetScenarioName = ""
			} else {
				m.phase = PhaseDisconnected
				m.targetScenarioName = ""
			}
			m.mu.Unlock()
			m.emitUpdate()
			return
		}
		m.mu.Lock()
		for i := range m.tasks {
			if m.tasks[i].State == TaskRunning || m.tasks[i].State == TaskCommitting {
				m.tasks[i].State = TaskFailed
			} else if m.tasks[i].State == TaskReady || m.tasks[i].State == TaskCommitted {
				m.tasks[i].State = TaskRolledBack
			}
		}
		if wasConnected && prevBox != nil {
			// Candidate switch failed -> preserve previous connection seamlessly!
			m.phase = PhaseConnected
			m.targetScenarioName = ""
			m.switchFailure = &SwitchFailureInfo{
				FromScenario: prevScenarioName,
				ToScenario:   plan.Scenario.Name,
				Message:      rawErr,
			}
		} else {
			_ = netwin.DisableSystemProxy(m.store.RootDir())
			m.phase = PhaseFailed
			m.targetScenarioName = ""
			title := rawErr
			if taskName != "" {
				title = fmt.Sprintf("%s：%s", taskName, rawErr)
			}
			ingressFact := "系统接管未提交"
			if takeoverSubmitted {
				ingressFact = "系统接管已撤销"
			}
			m.failure = &FailureDetails{
				Title:        title,
				RawError:     rawErr,
				IngressFact:  ingressFact,
				RollbackFact: "已回滚，原网络可用",
				RollbackOK:   true,
			}
		}
		m.mu.Unlock()
		m.emitUpdate()
	}

	// Step 1: Capture Windows Underlay
	m.mu.Lock()
	m.setTaskStateLocked("underlay:system", TaskRunning)
	m.mu.Unlock()
	m.emitUpdate()

	underlay, err := netwin.SnapshotUnderlay()
	if err != nil {
		failTx("系统基础网络", err.Error(), false)
		return
	}
	m.mu.Lock()
	m.systemFacts = *underlay
	m.mu.Unlock()

	if desiredMode == store.IngressWintun {
		if !underlay.IsElevated {
			failTx("Wintun 虚拟网卡", "TUN 模式需要以管理员身份运行 XDial（或改用「系统代理 / 仅 SOCKS5」模式）", false)
			return
		}
		if !underlay.HasWintunDLL {
			failTx("Wintun 虚拟网卡", "未检测到 wintun.dll，请将 wintun.dll 放置在程序目录或改用「系统代理」模式", false)
			return
		}
	}

	m.mu.Lock()
	m.setTaskStateLocked("underlay:system", TaskReady)
	for i := range m.tasks {
		if m.tasks[i].Kind == config.ConnectionTaskRuleSet || m.tasks[i].Kind == config.ConnectionTaskDNS {
			m.tasks[i].State = TaskRunning
		}
	}
	m.mu.Unlock()
	m.emitUpdate()

	if ctx.Err() != nil {
		failTx("", "已取消", false)
		return
	}

	// Step 2: Compile config & materialize remote RuleSets
	dnsBytes, _ := json.Marshal(underlay.DNSServers)
	runtimeDir := m.store.RuntimeDir()
	rawAndroidCfg, err := libbox.GenerateAndroidConfig(rtJSON, string(dnsBytes), runtimeDir)
	if err != nil {
		// Materialize any uncached remote URL RuleSets via Direct/fetchLine first,
		// then re-run GenerateAndroidConfig so public DNS still uses Windows system DNS.
		neCfg, neErr := libbox.GenerateNEConfig(rtJSON, "", runtimeDir)
		if neErr != nil {
			failTx("规则与配置编译", neErr.Error(), false)
			return
		}
		if refreshedCfg, aErr := libbox.GenerateAndroidConfig(rtJSON, string(dnsBytes), runtimeDir); aErr == nil {
			rawAndroidCfg = refreshedCfg
		} else {
			rawAndroidCfg = neCfg
		}
	}

	// Stop previous Box before starting candidate to avoid loopback port conflicts
	// and sslcon process-global AnyConnect singleton collisions.
	if wasConnected && prevBox != nil {
		_ = prevBox.Stop()
		prevBox = nil
		wasConnected = false
	}

	winConfigJSON, err := adaptConfigForWindows([]byte(rawAndroidCfg), desiredMode, alsoSOCKS, desiredPort, underlay.DefaultInterfaceName)
	if err != nil {
		failTx("数据面配置", err.Error(), false)
		return
	}

	m.mu.Lock()
	for i := range m.tasks {
		if m.tasks[i].Kind == config.ConnectionTaskRuleSet || m.tasks[i].Kind == config.ConnectionTaskDNS {
			m.tasks[i].State = TaskReady
		} else if m.tasks[i].Kind == config.ConnectionTaskLine || m.tasks[i].Kind == config.ConnectionTaskDataPlane {
			m.tasks[i].State = TaskRunning
		}
	}
	m.mu.Unlock()
	m.emitUpdate()

	// Step 3: Start in-process Libbox (sing-box + sslcon AnyConnect + Tailscale)
	candidate := libbox.New(&boxCallback{mgr: m})
	if underlay.DefaultInterfaceName != "" {
		candidate.SetDefaultInterface(underlay.DefaultInterfaceName, underlay.DefaultInterfaceIndex)
	}
	if ifacesJSON, mErr := json.Marshal(underlay.Interfaces); mErr == nil {
		_ = candidate.SetNetworkInterfaces(string(ifacesJSON))
	}
	if desiredMode == store.IngressWintun {
		candidate.SetTunOpener(func(opts *tun.Options, _ option.TunPlatformOptions) (tun.Tun, error) {
			return tun.New(*opts)
		})
	}

	if vpnLine := prof.ActiveVPNLine(); vpnLine != nil {
		if strings.TrimSpace(vpnLine.VPNServer) == "" || strings.TrimSpace(vpnLine.VPNUsername) == "" || vpnLine.VPNPassword == "" {
			failTx(vpnLine.Name, "AnyConnect 账号或密码未填写", false)
			return
		}
		err = candidate.StartResolvedWithInsecure(
			vpnLine.VPNServer,
			"",
			vpnLine.VPNUsername,
			vpnLine.VPNPassword,
			vpnLine.AllowInsecure,
			winConfigJSON,
		)
	} else {
		err = candidate.StartStandalone(winConfigJSON)
	}
	if err != nil {
		failTx("核心数据面", err.Error(), false)
		return
	}

	if ctx.Err() != nil {
		_ = candidate.Stop()
		failTx("", "已取消", false)
		return
	}

	// Step 4: Commit Windows Ingress (System Proxy or Local SOCKS5 or Wintun)
	m.mu.Lock()
	for i := range m.tasks {
		if m.tasks[i].Kind == config.ConnectionTaskLine || m.tasks[i].Kind == config.ConnectionTaskDataPlane {
			m.tasks[i].State = TaskReady
		}
		if m.tasks[i].Kind == config.ConnectionTaskIngress {
			m.tasks[i].State = TaskCommitting
		}
	}
	m.mu.Unlock()
	m.emitUpdate()

	if desiredMode == store.IngressSystemProxy {
		if err := netwin.EnableSystemProxy(m.store.RootDir(), desiredPort); err != nil {
			_ = candidate.Stop()
			failTx("Windows 系统代理", err.Error(), true)
			return
		}
	} else {
		_ = netwin.DisableSystemProxy(m.store.RootDir())
	}

	if prevBox != nil {
		_ = prevBox.Stop()
	}

	// Build active lines & outbound tag map
	catalog := config.BuildLineRuntimeCatalog(prof)
	tagMap := map[string]string{"direct": "direct"}
	for _, entry := range catalog.Lines {
		tagMap[entry.ID] = entry.Tag
	}
	var activeLines []ActiveLineStatus
	for _, t := range plan.Tasks {
		if t.Kind == config.ConnectionTaskLine {
			lType := t.ResourceType
			activeLines = append(activeLines, ActiveLineStatus{
				ID:   t.ResourceID,
				Name: t.Name,
				Type: lType,
			})
		}
	}

	nowMs := time.Now().UnixMilli()
	m.mu.Lock()
	for i := range m.tasks {
		m.tasks[i].State = TaskCommitted
	}
	m.activeBox = candidate
	m.activeScenarioID = plan.Scenario.ID
	m.activeScenarioName = plan.Scenario.Name
	m.activeFingerprint = plan.ConfigurationFingerprint
	m.activeIngressMode = desiredMode
	m.activeSOCKSPort = desiredPort
	m.activeLineOutbounds = tagMap
	m.activeLines = activeLines
	if m.connectedAt == nil {
		m.connectedAt = &nowMs
	}
	zero := 0.0
	m.downloadBps = &zero
	m.uploadBps = &zero
	m.hasBaselineSample = false
	m.phase = PhaseConnected
	m.targetScenarioName = ""
	m.connectCancel = nil
	m.mu.Unlock()
	m.emitUpdate()

	// Asynchronously probe observed exit IPv4 and initial latency for active lines (bounded, non-blocking)
	go m.probeActiveLineFacts(candidate, activeLines, tagMap)
}

func (m *Manager) probeActiveLineFacts(instance *libbox.Libbox, lines []ActiveLineStatus, tagMap map[string]string) {
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, line := range lines {
		line := line
		tag := tagMap[line.ID]
		if tag == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ip4, _ := instance.ProbeOutboundIPForFamily(tag, "ipv4", 4000)
			ms, latErr := instance.TestOutbound(tag, "", 5000)

			m.mu.Lock()
			if m.activeBox == instance {
				for i := range m.activeLines {
					if m.activeLines[i].ID == line.ID && ip4 != "" {
						m.activeLines[i].ExitIPv4 = ip4
					}
				}
				if latErr == nil && ms > 0 {
					msVal := ms
					m.latencies[line.ID] = LatencyRecord{
						Milliseconds: &msVal,
						ObservedAt:   time.Now().UnixMilli(),
					}
				}
			}
			m.mu.Unlock()
			m.emitUpdate()
		}()
	}
	wg.Wait()
}

func (m *Manager) CancelConnect() error {
	m.mu.Lock()
	cancel := m.connectCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *Manager) Disconnect() error {
	m.mu.Lock()
	if m.connectCancel != nil {
		m.connectCancel()
		m.connectCancel = nil
	}
	boxToStop := m.activeBox
	m.activeBox = nil
	m.phase = PhaseDisconnecting
	m.mu.Unlock()
	m.emitUpdate()

	_ = netwin.DisableSystemProxy(m.store.RootDir())
	if boxToStop != nil {
		_ = boxToStop.Stop()
	}

	m.mu.Lock()
	m.phase = PhaseDisconnected
	m.targetScenarioName = ""
	m.connectedAt = nil
	m.downloadBps = nil
	m.uploadBps = nil
	m.hasBaselineSample = false
	m.activeLines = nil
	m.switchFailure = nil
	m.parkedRecovery = false
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) DismissSwitchFailure() {
	m.mu.Lock()
	m.switchFailure = nil
	m.mu.Unlock()
	m.emitUpdate()
}

func (m *Manager) DismissOnboarding() error {
	m.mu.Lock()
	m.prefs.OnboardingDismissed = true
	err := m.store.SavePreferences(m.prefs)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func selectPreferredVPNScenarioID(prof *config.Profile, vpnLineID string) string {
	if prof == nil {
		return ""
	}
	scenarioUsesVPN := func(sc config.Scenario) bool {
		if sc.DefaultLineID == vpnLineID {
			return true
		}
		for _, b := range sc.Bindings {
			if b.LineID == vpnLineID {
				return true
			}
		}
		return false
	}
	// If current active scenario already uses this VPN line, keep it!
	if cur := prof.ActiveScenario(); cur != nil && scenarioUsesVPN(*cur) {
		return cur.ID
	}
	// Prefer domestic/office scenario ("中国内地", "在国内", "办公") when signing into Company VPN
	for _, kw := range []string{"中国内地", "在国内", "本地区", "办公"} {
		for _, sc := range prof.Scenarios {
			if scenarioUsesVPN(sc) && strings.Contains(sc.Name, kw) {
				return sc.ID
			}
		}
	}
	for _, sc := range prof.Scenarios {
		if scenarioUsesVPN(sc) {
			return sc.ID
		}
	}
	return prof.ActiveScenarioID
}

// SignInAndConnect mirrors iOS SignInSheet.submit: cleanly disconnects any prior
// session if needed, saves AnyConnect credentials (or creates the Company VPN line
// & scenario on a blank profile), ensures a VPN-enabled scenario is active, and
// immediately initiates the connection transaction.
func (m *Manager) SignInAndConnect(lineID, server, username, password string) error {
	server = strings.TrimSpace(server)
	username = strings.TrimSpace(username)
	if server == "" {
		return fmt.Errorf("请填写服务器地址")
	}
	if username == "" || password == "" {
		return fmt.Errorf("请填写公司账号用户名与密码")
	}

	m.mu.Lock()
	needDisconnect := m.phase != PhaseDisconnected && m.phase != PhaseFailed
	m.mu.Unlock()
	if needDisconnect {
		_ = m.Disconnect()
	}

	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, editingSourceID string) error {
		targetVPNID := ""
		if lineID != "" {
			for i := range prof.Lines {
				if prof.Lines[i].ID == lineID {
					prof.Lines[i].VPNServer = server
					prof.Lines[i].VPNUsername = username
					prof.Lines[i].VPNPassword = password
					prof.Lines[i].Enabled = true
					targetVPNID = prof.Lines[i].ID
					break
				}
			}
		}
		if targetVPNID == "" {
			for i := range prof.Lines {
				if prof.Lines[i].Type == config.LineTypeVPN {
					prof.Lines[i].VPNServer = server
					prof.Lines[i].VPNUsername = username
					prof.Lines[i].VPNPassword = password
					prof.Lines[i].Enabled = true
					targetVPNID = prof.Lines[i].ID
					break
				}
			}
		}
		if targetVPNID != "" {
			prof.ActiveScenarioID = selectPreferredVPNScenarioID(prof, targetVPNID)
			return nil
		}

		// Otherwise create a fresh Company VPN line + standard scenarios on blank start
		newLineID := editingSourceID + "-vpn-" + randomHex(4)
		prof.Lines = append(prof.Lines, config.Line{
			ID:                newLineID,
			Name:              "公司 VPN",
			Type:              config.LineTypeVPN,
			Enabled:           true,
			IdentityProfileID: editingSourceID,
			VPNServer:         server,
			VPNUsername:       username,
			VPNPassword:       password,
		})
		officeScID := "scenario-office-" + randomHex(4)
		prof.Scenarios = append(prof.Scenarios, config.Scenario{
			ID:            officeScID,
			Name:          "办公",
			Icon:          "office",
			DefaultLineID: newLineID,
			Bindings:      []config.RuleBinding{},
		})
		prof.ActiveScenarioID = officeScID
		return nil
	})
	if err == nil {
		m.failure = nil
		m.switchFailure = nil
		m.prefs.OnboardingDismissed = true
		_ = m.store.SavePreferences(m.prefs)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return m.Connect()
}

func (m *Manager) SelectScenario(scenarioID string) error {
	m.mu.Lock()
	m.prefs.AutomaticScenario = false
	_ = m.store.SavePreferences(m.prefs)
	if err := m.applyActiveScenarioIDLocked(scenarioID); err != nil {
		m.mu.Unlock()
		return err
	}
	shouldSwitch := m.phase == PhaseConnected
	m.mu.Unlock()
	m.emitUpdate()

	if shouldSwitch {
		return m.startTransaction(true)
	}
	return nil
}

func (m *Manager) SetAutomaticScenario(enabled bool) error {
	m.mu.Lock()
	m.prefs.AutomaticScenario = enabled
	_ = m.store.SavePreferences(m.prefs)
	switched := false
	if enabled {
		switched = m.evaluateAutoScenarioLocked()
	} else {
		m.autoReason = ""
	}
	shouldSwitch := switched && m.phase == PhaseConnected
	m.mu.Unlock()
	m.emitUpdate()

	if shouldSwitch {
		return m.startTransaction(true)
	}
	return nil
}

func (m *Manager) SavePreferences(prefs store.Preferences) error {
	if err := m.store.SavePreferences(prefs); err != nil {
		return err
	}
	_ = netwin.SetLaunchAtLogin(prefs.LaunchAtLogin)
	m.mu.Lock()
	m.prefs = m.store.LoadPreferences()
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

// --- Profile Source Operations ---

func (m *Manager) SelectProfileSource(sourceID string) error {
	m.mu.Lock()
	nextLib, err := libbox.SelectProfileLibrarySource(m.libraryJSON, sourceID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) CreateBlankProfile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("请输入配置名称")
	}
	blank := config.Profile{
		Lines: []config.Line{{ID: "direct", Name: "直连", Type: config.LineTypeDirect, Enabled: true}},
		Scenarios: []config.Scenario{{
			ID:            "default-" + randomHex(4),
			Name:          name,
			DefaultLineID: "direct",
		}},
	}
	blank.ActiveScenarioID = blank.Scenarios[0].ID
	raw, _ := json.Marshal(blank)
	return m.ImportProfileContent(name, string(raw))
}

const (
	defaultCNGeositeSRSURL = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/cn.srs"
	defaultCNGeoIPSRSURL   = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geoip/cn.srs"
)

func isConnectivityAcceptanceRule(rs config.RuleSet) bool {
	if rs.ID == "xdial-connectivity-direct" || rs.ID == "xdial-connectivity-anyconnect" {
		return true
	}
	name := strings.TrimSpace(rs.Name)
	return name == "连接验收 · Direct" || name == "连接验收 · 非 Direct 出口"
}

func restoreMobileRedactedRuleSet(rs *config.RuleSet) bool {
	if rs == nil || rs.Type != config.RuleSetTypeURL || strings.TrimSpace(rs.URL) != "" {
		return false
	}
	name := strings.TrimSpace(rs.Name)
	switch {
	case name == "国内网站" || rs.ID == "geosite-cn":
		rs.URL = defaultCNGeositeSRSURL
		rs.Format = "srs"
		rs.Enabled = true
		return true
	case name == "国内 IP" || name == "国内IP" || rs.ID == "geoip-cn":
		rs.URL = defaultCNGeoIPSRSURL
		rs.Format = "srs"
		rs.Enabled = true
		return true
	default:
		return false
	}
}

func choosePreferredImportedScenarioID(prof *config.Profile) string {
	if prof == nil || len(prof.Scenarios) == 0 {
		return ""
	}
	for _, sc := range prof.Scenarios {
		if sc.ID == prof.ActiveScenarioID && sc.ID != "daily-direct" {
			return sc.ID
		}
	}
	for _, kw := range []string{"中国内地", "在国内", "本地区", "办公"} {
		for _, sc := range prof.Scenarios {
			if strings.Contains(sc.Name, kw) {
				return sc.ID
			}
		}
	}
	for _, sc := range prof.Scenarios {
		if sc.DefaultLineID != "" && sc.DefaultLineID != "direct" {
			return sc.ID
		}
		for _, b := range sc.Bindings {
			if b.LineID != "" && b.LineID != "direct" {
				return sc.ID
			}
		}
	}
	return prof.Scenarios[0].ID
}

func sanitizeMobileProfileJSON(raw []byte) (string, bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil || probe["lines"] == nil {
		return string(raw), false
	}
	prof, err := config.ParseProfile(raw)
	if err != nil || prof == nil {
		return string(raw), false
	}
	prof.Subscriptions = nil
	for i := range prof.Lines {
		if prof.Lines[i].Type == "ss" {
			prof.Lines[i].Type = config.LineTypeShadowsocks
		}
	}
	removedRules := map[string]bool{}
	keptRules := make([]config.RuleSet, 0, len(prof.RuleSets))
	for _, rs := range prof.RuleSets {
		if isConnectivityAcceptanceRule(rs) {
			removedRules[rs.ID] = true
			continue
		}
		restoreMobileRedactedRuleSet(&rs)
		keptRules = append(keptRules, rs)
	}
	prof.RuleSets = keptRules

	for i := range prof.Scenarios {
		sc := &prof.Scenarios[i]
		if sc.DefaultSubscriptionID != "" {
			sc.DefaultSubscriptionID = ""
			if sc.DefaultLineID == "" {
				sc.DefaultLineID = "direct"
			}
		}
		if len(sc.Bindings) > 0 {
			filtered := make([]config.RuleBinding, 0, len(sc.Bindings))
			for _, b := range sc.Bindings {
				if removedRules[b.RuleSetID] || b.SubscriptionID != "" {
					continue
				}
				filtered = append(filtered, b)
			}
			sc.Bindings = filtered
		}
		if len(sc.MatchOrder) > 0 {
			filteredOrder := make([]string, 0, len(sc.MatchOrder))
			for _, id := range sc.MatchOrder {
				if !removedRules[id] {
					filteredOrder = append(filteredOrder, id)
				}
			}
			sc.MatchOrder = filteredOrder
		}
	}
	if prof.ActiveScenario() == nil && len(prof.Scenarios) > 0 {
		prof.ActiveScenarioID = choosePreferredImportedScenarioID(prof)
	}
	out, err := json.Marshal(prof)
	if err != nil {
		return string(raw), false
	}
	return string(out), true
}

func isUntouchedDefaultSeedLibrary(libJSON string) bool {
	var rawLib struct {
		Profiles []struct {
			ID      string          `json:"id"`
			Source  json.RawMessage `json:"source,omitempty"`
			Profile struct {
				Lines    []config.Line    `json:"lines"`
				RuleSets []config.RuleSet `json:"rule_sets"`
			} `json:"profile"`
		} `json:"profiles"`
		Scenarios []config.Scenario `json:"scenarios"`
	}
	if json.Unmarshal([]byte(libJSON), &rawLib) != nil {
		return false
	}
	if len(rawLib.Profiles) != 1 || rawLib.Profiles[0].ID != "default" {
		return false
	}
	if len(rawLib.Scenarios) != 1 || rawLib.Scenarios[0].ID != "daily-direct" {
		return false
	}
	for _, l := range rawLib.Profiles[0].Profile.Lines {
		if l.ID != "direct" {
			return false
		}
	}
	for _, r := range rawLib.Profiles[0].Profile.RuleSets {
		if r.ID != "lan-private" {
			return false
		}
	}
	return true
}

// repairImportedMobileLibraryLocked cleans up legacy mobile-only connectivity rules,
// restores mobile-redacted CN SRS URLs, and removes an untouched initial "日常直连"
// seed if another profile source was already imported alongside it, while preserving
// all DPAPI-saved credentials.
func (m *Manager) repairImportedMobileLibraryLocked() {
	var rawLib struct {
		Profiles []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Profile struct {
				Lines    []config.Line    `json:"lines"`
				RuleSets []config.RuleSet `json:"rule_sets"`
			} `json:"profile"`
		} `json:"profiles"`
	}
	if json.Unmarshal([]byte(m.libraryJSON), &rawLib) != nil {
		return
	}

	defaultUntouched := false
	var nonDefaultNames []string
	for _, p := range rawLib.Profiles {
		if p.ID == "default" {
			onlyDirect := true
			for _, l := range p.Profile.Lines {
				if l.ID != "direct" {
					onlyDirect = false
					break
				}
			}
			onlyDefaultRule := true
			for _, r := range p.Profile.RuleSets {
				if r.ID != "lan-private" {
					onlyDefaultRule = false
					break
				}
			}
			defaultUntouched = onlyDirect && onlyDefaultRule
		} else {
			nonDefaultNames = append(nonDefaultNames, p.Name)
		}
	}
	canDropDefault := defaultUntouched && len(nonDefaultNames) > 0
	singleImportedPrefix := ""
	if canDropDefault && len(nonDefaultNames) == 1 && strings.TrimSpace(nonDefaultNames[0]) != "" {
		singleImportedPrefix = strings.TrimSpace(nonDefaultNames[0]) + " · "
	}

	mutated := false
	_ = m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		removedRules := map[string]bool{}
		nextRules := make([]config.RuleSet, 0, len(prof.RuleSets))
		for _, rs := range prof.RuleSets {
			if isConnectivityAcceptanceRule(rs) {
				removedRules[rs.ID] = true
				mutated = true
				continue
			}
			if canDropDefault && rs.ID == "lan-private" {
				removedRules[rs.ID] = true
				mutated = true
				continue
			}
			if restoreMobileRedactedRuleSet(&rs) {
				mutated = true
			}
			nextRules = append(nextRules, rs)
		}

		var nextScenarios []config.Scenario
		for _, sc := range prof.Scenarios {
			if canDropDefault && sc.ID == "daily-direct" && len(prof.Scenarios) > 1 {
				mutated = true
				continue
			}
			if singleImportedPrefix != "" && strings.HasPrefix(sc.Name, singleImportedPrefix) {
				sc.Name = strings.TrimPrefix(sc.Name, singleImportedPrefix)
				mutated = true
			}
			if len(removedRules) > 0 && len(sc.Bindings) > 0 {
				filtered := make([]config.RuleBinding, 0, len(sc.Bindings))
				for _, b := range sc.Bindings {
					if removedRules[b.RuleSetID] {
						mutated = true
						continue
					}
					filtered = append(filtered, b)
				}
				sc.Bindings = filtered
			}
			if len(removedRules) > 0 && len(sc.MatchOrder) > 0 {
				filteredOrder := make([]string, 0, len(sc.MatchOrder))
				for _, id := range sc.MatchOrder {
					if !removedRules[id] {
						filteredOrder = append(filteredOrder, id)
					}
				}
				sc.MatchOrder = filteredOrder
			}
			nextScenarios = append(nextScenarios, sc)
		}
		if len(nextScenarios) == 0 {
			return fmt.Errorf("no scenarios remaining")
		}
		prof.RuleSets = nextRules
		prof.Scenarios = nextScenarios
		if prof.ActiveScenario() == nil {
			prof.ActiveScenarioID = choosePreferredImportedScenarioID(prof)
			mutated = true
		}
		if !mutated {
			return fmt.Errorf("noop")
		}
		return nil
	})

	if canDropDefault {
		if cleaned, err := libbox.RemoveProfileLibrarySource(m.libraryJSON, "default"); err == nil {
			if m.store.SaveLibrary(cleaned) == nil {
				m.libraryJSON = cleaned
			}
		}
	}
}

// unwrapMobileEnvelope unwraps iOS "xdial-mobile-configuration" exports and
// "xdial-mobile-seed" Seed.json envelopes into the inner Profile JSON expected
// by PreviewNativeProfileImport, stripping mobile connectivity rules and restoring
// mobile-redacted standard CN SRS URLs.
func unwrapMobileEnvelope(content string, fallbackName string) (unwrapped string, inferredName string) {
	inferredName = fallbackName
	var env struct {
		Format      string          `json:"format"`
		ProfileName string          `json:"profile_name"`
		Profile     json.RawMessage `json:"profile"`
	}
	if json.Unmarshal([]byte(content), &env) == nil && len(env.Profile) > 0 &&
		(env.Format == "xdial-mobile-configuration" || env.Format == "xdial-mobile-seed" || env.Format == "") {
		if (inferredName == "" || inferredName == "导入配置") && strings.TrimSpace(env.ProfileName) != "" {
			inferredName = strings.TrimSpace(env.ProfileName)
		}
		cleaned, _ := sanitizeMobileProfileJSON(env.Profile)
		return cleaned, inferredName
	}
	cleaned, _ := sanitizeMobileProfileJSON([]byte(content))
	return cleaned, inferredName
}

func shouldReplaceBlankSeedWithImportedProfile(content string) bool {
	prof, err := config.ParseProfile([]byte(content))
	if err != nil || prof == nil || len(prof.Scenarios) == 0 {
		return false
	}
	if len(prof.Scenarios) > 1 {
		return true
	}
	for _, l := range prof.Lines {
		if l.ID != "direct" {
			return true
		}
	}
	return false
}

func (m *Manager) ImportProfileContent(name, content string) error {
	customName := strings.TrimSpace(name)
	name = customName
	if name == "" {
		name = "导入配置"
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("配置内容为空")
	}
	content, name = unwrapMobileEnvelope(content, name)

	m.mu.Lock()
	curLib := m.libraryJSON
	m.mu.Unlock()

	replaceBlank := isUntouchedDefaultSeedLibrary(curLib) && shouldReplaceBlankSeedWithImportedProfile(content)

	// Validate via native XDial / sing-box JSON preview first
	previewJSON, err := libbox.PreviewNativeProfileImport(curLib, content, name, "")
	if err != nil {
		replaceBlank = false
		// Fall back to Clash YAML / Surge / Base64 URI subscription parser!
		parsed, subErr := subscription.Parse("", content, "")
		if subErr != nil {
			return fmt.Errorf("无法解析配置（支持 XDial/sing-box JSON、Clash YAML、Surge 或节点链接）: %w", err)
		}
		if customName == "" && len(parsed.Lines) == 1 && strings.TrimSpace(parsed.Lines[0].Name) != "" {
			name = strings.TrimSpace(parsed.Lines[0].Name)
		}
		subscription.ExpandRulesets(parsed)
		sourceID, idErr := config.NewProfileSourceID()
		if idErr != nil {
			return idErr
		}
		importedProf, impErr := config.ImportSubscriptionProfile(sourceID, parsed.Lines, parsed.ProxyGroups, parsed.Rules, false)
		if impErr != nil {
			// Retry nodes-only if provider rules referenced custom groups
			importedProf, impErr = config.ImportSubscriptionProfile(sourceID, parsed.Lines, nil, nil, true)
			if impErr != nil {
				return impErr
			}
		}
		encoded, _ := json.Marshal(importedProf)
		previewJSON, err = config.PreviewProfileLibraryImport(curLib, string(encoded), name, sourceID, false)
		if err != nil {
			return err
		}
	}

	m.mu.Lock()
	if replaceBlank && isUntouchedDefaultSeedLibrary(m.libraryJSON) {
		if cleanLib, nErr := libbox.NewProfileLibrary(content); nErr == nil {
			if name != "" && name != "导入配置" {
				var raw map[string]any
				if json.Unmarshal([]byte(cleanLib), &raw) == nil {
					if profiles, ok := raw["profiles"].([]any); ok && len(profiles) == 1 {
						if rec, ok := profiles[0].(map[string]any); ok {
							rec["name"] = name
							if renamed, mErr := json.Marshal(raw); mErr == nil {
								cleanLib = string(renamed)
							}
						}
					}
				}
			}
			if err := m.store.SaveLibrary(cleanLib); err != nil {
				m.mu.Unlock()
				return err
			}
			m.libraryJSON = cleanLib
			m.mu.Unlock()
			m.emitUpdate()
			return nil
		}
	}

	nextLib, err := libbox.CommitProfileLibraryPreview(m.libraryJSON, previewJSON)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.repairImportedMobileLibraryLocked()
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) ImportSubscriptionURL(name, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if subscription.IsShareURI(rawURL) {
		return m.ImportProfileContent(name, rawURL)
	}
	if err := libbox.ValidateProfileSourceURL(rawURL); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = "订阅配置"
	}
	bodyBytes, err := subscription.FetchStrictBytes(rawURL, 4*1024*1024)
	if err != nil {
		return fmt.Errorf("下载订阅失败: %w", err)
	}
	content := string(bodyBytes)

	m.mu.Lock()
	curLib := m.libraryJSON
	m.mu.Unlock()

	sourceID, err := config.NewProfileSourceID()
	if err != nil {
		return err
	}

	var encodedProf []byte
	if prof, docErr := config.ImportProfileDocument(bodyBytes, sourceID); docErr == nil {
		encodedProf, _ = json.Marshal(prof)
	} else {
		parsed, subErr := subscription.Parse(rawURL, content, "")
		if subErr != nil {
			return fmt.Errorf("解析订阅内容失败: %w", subErr)
		}
		subscription.ExpandRulesets(parsed)
		importedProf, impErr := config.ImportSubscriptionProfile(sourceID, parsed.Lines, parsed.ProxyGroups, parsed.Rules, false)
		if impErr != nil {
			importedProf, impErr = config.ImportSubscriptionProfile(sourceID, parsed.Lines, nil, nil, true)
			if impErr != nil {
				return impErr
			}
		}
		encodedProf, _ = json.Marshal(importedProf)
	}

	previewJSON, err := config.PreviewProfileLibraryRemoteImport(curLib, string(encodedProf), name, sourceID, false, rawURL, "direct")
	if err != nil {
		return err
	}

	m.mu.Lock()
	nextLib, err := libbox.CommitProfileLibraryPreview(m.libraryJSON, previewJSON)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) RefreshProfileSource(sourceID string) error {
	m.mu.Lock()
	remoteJSON, err := libbox.ProfileLibraryRemoteSource(m.libraryJSON, sourceID)
	curLib := m.libraryJSON
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("该配置不是在线订阅来源")
	}
	var meta struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal([]byte(remoteJSON), &meta); err != nil || meta.URL == "" {
		return fmt.Errorf("订阅地址无效")
	}
	bodyBytes, err := subscription.FetchStrictBytes(meta.URL, 4*1024*1024)
	if err != nil {
		return fmt.Errorf("刷新订阅失败: %w", err)
	}

	var encodedProf []byte
	if prof, docErr := config.ImportProfileDocument(bodyBytes, sourceID); docErr == nil {
		encodedProf, _ = json.Marshal(prof)
	} else {
		parsed, subErr := subscription.Parse(meta.URL, string(bodyBytes), "")
		if subErr != nil {
			return subErr
		}
		subscription.ExpandRulesets(parsed)
		importedProf, impErr := config.ImportSubscriptionProfile(sourceID, parsed.Lines, parsed.ProxyGroups, parsed.Rules, false)
		if impErr != nil {
			importedProf, impErr = config.ImportSubscriptionProfile(sourceID, parsed.Lines, nil, nil, true)
			if impErr != nil {
				return impErr
			}
		}
		encodedProf, _ = json.Marshal(importedProf)
	}

	previewJSON, err := config.PreviewProfileLibraryRemoteImport(curLib, string(encodedProf), meta.Name, sourceID, true, meta.URL, "direct")
	if err != nil {
		return err
	}
	m.mu.Lock()
	nextLib, err := libbox.CommitProfileLibraryPreview(m.libraryJSON, previewJSON)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) RenameProfileSource(sourceID, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("名称不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var raw map[string]any
	if err := json.Unmarshal([]byte(m.libraryJSON), &raw); err != nil {
		return err
	}
	profiles, _ := raw["profiles"].([]any)
	found := false
	for _, item := range profiles {
		if rec, ok := item.(map[string]any); ok && rec["id"] == sourceID {
			rec["name"] = newName
			found = true
		}
	}
	if !found {
		return fmt.Errorf("未找到指定配置来源")
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := m.store.SaveLibrary(string(updated)); err != nil {
		return err
	}
	m.libraryJSON = string(updated)
	go m.emitUpdate()
	return nil
}

func (m *Manager) ExportProfileSource(sourceID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(rtJSON), &doc); err != nil {
		return "", err
	}
	delete(doc, "library_revision")
	delete(doc, "profile_id")
	pretty, err := json.MarshalIndent(doc, "", "  ")
	return string(pretty), err
}

func (m *Manager) DeleteProfileSource(sourceID string) error {
	m.mu.Lock()
	nextLib, err := libbox.RemoveProfileLibrarySource(m.libraryJSON, sourceID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

func (m *Manager) ResetToDefaults() error {
	m.mu.Lock()
	if m.phase != PhaseDisconnected {
		m.mu.Unlock()
		return fmt.Errorf("请先断开当前连接再恢复默认配置")
	}
	nextLib, err := m.store.ResetToDefaults()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.libraryJSON = nextLib
	m.mu.Unlock()
	m.emitUpdate()
	return nil
}

// --- Resource CRUD Helpers ---

func (m *Manager) mutateRuntimeLocked(fn func(prof *config.Profile, editingSourceID string) error) error {
	var libMeta struct {
		EditingProfileID string `json:"editingProfileID"`
	}
	_ = json.Unmarshal([]byte(m.libraryJSON), &libMeta)

	rtJSON, err := libbox.ProfileLibraryRuntime(m.libraryJSON)
	if err != nil {
		return err
	}
	prof, err := config.ParseRuntimeProfile([]byte(rtJSON))
	if err != nil {
		return err
	}
	var rawDoc map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rtJSON), &rawDoc)

	if err := fn(prof, libMeta.EditingProfileID); err != nil {
		return err
	}

	rawDoc["lines"], _ = json.Marshal(prof.Lines)
	rawDoc["rule_sets"], _ = json.Marshal(prof.RuleSets)
	rawDoc["scenarios"], _ = json.Marshal(prof.Scenarios)
	rawDoc["active_scenario_id"], _ = json.Marshal(prof.ActiveScenarioID)

	draftBytes, err := json.Marshal(rawDoc)
	if err != nil {
		return err
	}
	nextLib, err := libbox.ApplyProfileLibraryRuntime(m.libraryJSON, string(draftBytes))
	if err != nil {
		return err
	}
	if err := m.store.SaveLibrary(nextLib); err != nil {
		return err
	}
	m.libraryJSON = nextLib
	return nil
}

func (m *Manager) SaveLine(line config.Line) error {
	if line.ID == "direct" {
		return fmt.Errorf("直连线路不可修改")
	}
	line.Name = strings.TrimSpace(line.Name)
	if line.Name == "" {
		return fmt.Errorf("线路名称不能为空")
	}
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, editingSourceID string) error {
		if line.ID == "" {
			line.ID = editingSourceID + "-line-" + randomHex(6)
			line.IdentityProfileID = editingSourceID
			prof.Lines = append(prof.Lines, line)
			return nil
		}
		for i := range prof.Lines {
			if prof.Lines[i].ID == line.ID {
				line.IdentityProfileID = prof.Lines[i].IdentityProfileID
				line.IdentityHostname = prof.Lines[i].IdentityHostname
				prof.Lines[i] = line
				return nil
			}
		}
		line.IdentityProfileID = editingSourceID
		prof.Lines = append(prof.Lines, line)
		return nil
	})
	delete(m.latencies, line.ID)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) ParseLineURIs(raw string) ([]config.Line, error) {
	return subscription.ParseShareURIs(raw)
}

func (m *Manager) ImportLineURIs(raw string) ([]config.Line, error) {
	parsed, err := subscription.ParseShareURIs(raw)
	if err != nil {
		return nil, err
	}
	var added []config.Line
	m.mu.Lock()
	mutErr := m.mutateRuntimeLocked(func(prof *config.Profile, editingSourceID string) error {
		for _, item := range parsed {
			item.ID = editingSourceID + "-line-" + randomHex(6)
			item.IdentityProfileID = editingSourceID
			item.Enabled = true
			prof.Lines = append(prof.Lines, item)
			added = append(added, item)
		}
		return nil
	})
	m.mu.Unlock()
	if mutErr != nil {
		return nil, mutErr
	}
	m.emitUpdate()
	return added, nil
}

func (m *Manager) DeleteLine(lineID string) error {
	if lineID == "direct" {
		return fmt.Errorf("直连线路不可删除")
	}
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		next := make([]config.Line, 0, len(prof.Lines))
		for _, l := range prof.Lines {
			if l.ID != lineID {
				next = append(next, l)
			}
		}
		prof.Lines = next
		return nil
	})
	delete(m.latencies, lineID)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) ReorderLines(orderedIDs []string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		pos := make(map[string]int, len(orderedIDs))
		for i, id := range orderedIDs {
			pos[id] = i
		}
		// Stable reorder only among items in orderedIDs
		var subset []config.Line
		var indices []int
		for idx, l := range prof.Lines {
			if _, ok := pos[l.ID]; ok {
				subset = append(subset, l)
				indices = append(indices, idx)
			}
		}
		ordered := make([]config.Line, len(subset))
		copy(ordered, subset)
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if pos[ordered[j].ID] < pos[ordered[i].ID] {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
		for k, idx := range indices {
			prof.Lines[idx] = ordered[k]
		}
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) SaveLineGroup(group config.Line) error {
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		return fmt.Errorf("线路组名称不能为空")
	}
	if group.Type != config.LineTypeSelector && group.Type != config.LineTypeURLTest {
		group.Type = config.LineTypeURLTest
	}
	group.Enabled = true
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		if group.ID == "" {
			group.ID = "group-" + randomHex(6)
			prof.Lines = append(prof.Lines, group)
			return nil
		}
		for i := range prof.Lines {
			if prof.Lines[i].ID == group.ID {
				prof.Lines[i] = group
				return nil
			}
		}
		prof.Lines = append(prof.Lines, group)
		return nil
	})
	activeBox := m.activeBox
	isConnected := m.phase == PhaseConnected
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if isConnected && activeBox != nil && group.Type == config.LineTypeSelector && group.GroupDefault != "" {
		outTag := "proxy-" + group.GroupDefault
		if group.GroupDefault == "direct" {
			outTag = "direct"
		}
		_ = activeBox.SelectOutbound("proxy-"+group.ID, outTag)
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) DeleteLineGroup(groupID string) error {
	return m.DeleteLine(groupID)
}

func (m *Manager) SaveRuleSet(rule config.RuleSet) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, editingSourceID string) error {
		if rule.ID == "" {
			rule.ID = editingSourceID + "-rule-" + randomHex(6)
			prof.RuleSets = append(prof.RuleSets, rule)
			return nil
		}
		for i := range prof.RuleSets {
			if prof.RuleSets[i].ID == rule.ID {
				prof.RuleSets[i] = rule
				return nil
			}
		}
		prof.RuleSets = append(prof.RuleSets, rule)
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) DeleteRuleSet(ruleID string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		for _, sc := range prof.Scenarios {
			for _, b := range sc.Bindings {
				if b.RuleSetID == ruleID {
					return fmt.Errorf("规则仍被场景「%s」引用，请先从场景移除", sc.Name)
				}
			}
		}
		next := make([]config.RuleSet, 0, len(prof.RuleSets))
		for _, r := range prof.RuleSets {
			if r.ID != ruleID {
				next = append(next, r)
			}
		}
		prof.RuleSets = next
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) ReorderRuleSets(orderedIDs []string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		pos := make(map[string]int, len(orderedIDs))
		for i, id := range orderedIDs {
			pos[id] = i
		}
		var subset []config.RuleSet
		var indices []int
		for idx, r := range prof.RuleSets {
			if _, ok := pos[r.ID]; ok {
				subset = append(subset, r)
				indices = append(indices, idx)
			}
		}
		ordered := make([]config.RuleSet, len(subset))
		copy(ordered, subset)
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if pos[ordered[j].ID] < pos[ordered[i].ID] {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
		for k, idx := range indices {
			prof.RuleSets[idx] = ordered[k]
		}
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) SaveScenario(sc config.Scenario) error {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" {
		return fmt.Errorf("场景名称不能为空")
	}
	if sc.DefaultLineID == "" {
		sc.DefaultLineID = "direct"
	}
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		// Enforce SSID uniqueness across scenarios (DESIGN.md §5.5)
		for _, other := range prof.Scenarios {
			if other.ID == sc.ID {
				continue
			}
			for _, existingSSID := range other.MatchSSIDs {
				for _, candidateSSID := range sc.MatchSSIDs {
					if existingSSID != "" && existingSSID == candidateSSID {
						return fmt.Errorf("Wi-Fi「%s」已归属场景「%s」", candidateSSID, other.Name)
					}
				}
			}
		}
		if sc.ID == "" {
			sc.ID = "scenario-" + randomHex(6)
			prof.Scenarios = append(prof.Scenarios, sc)
			if prof.ActiveScenarioID == "" {
				prof.ActiveScenarioID = sc.ID
			}
			return nil
		}
		for i := range prof.Scenarios {
			if prof.Scenarios[i].ID == sc.ID {
				prof.Scenarios[i] = sc
				return nil
			}
		}
		prof.Scenarios = append(prof.Scenarios, sc)
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) DuplicateScenario(scenarioID string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		for _, s := range prof.Scenarios {
			if s.ID == scenarioID {
				clone := s
				clone.ID = "scenario-" + randomHex(6)
				clone.Name = s.Name + " 副本"
				clone.MatchSSIDs = nil
				clone.Bindings = append([]config.RuleBinding(nil), s.Bindings...)
				prof.Scenarios = append(prof.Scenarios, clone)
				return nil
			}
		}
		return fmt.Errorf("未找到要复制的场景")
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) DeleteScenario(scenarioID string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		if len(prof.Scenarios) <= 1 {
			return fmt.Errorf("至少保留一个场景")
		}
		if prof.ActiveScenarioID == scenarioID {
			return fmt.Errorf("不能删除当前选中的场景，请先切换到其他场景")
		}
		next := make([]config.Scenario, 0, len(prof.Scenarios))
		for _, s := range prof.Scenarios {
			if s.ID != scenarioID {
				next = append(next, s)
			}
		}
		prof.Scenarios = next
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

func (m *Manager) ReorderScenarios(orderedIDs []string) error {
	m.mu.Lock()
	err := m.mutateRuntimeLocked(func(prof *config.Profile, _ string) error {
		pos := make(map[string]int, len(orderedIDs))
		for i, id := range orderedIDs {
			pos[id] = i
		}
		ordered := make([]config.Scenario, len(prof.Scenarios))
		copy(ordered, prof.Scenarios)
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if pos[ordered[j].ID] < pos[ordered[i].ID] {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
		prof.Scenarios = ordered
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.emitUpdate()
	return nil
}

// --- Latency Testing (Bounded concurrency 3, 5s timeout per ARCHITECTURE.md §2) ---

func (m *Manager) TestBatchLatency(lineIDs []string) error {
	m.mu.Lock()
	if m.latencyCancel != nil {
		m.latencyCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.latencyCancel = cancel

	prof, _ := m.buildEditorViewLocked()
	if prof == nil {
		m.mu.Unlock()
		return fmt.Errorf("配置不可用")
	}

	var targets []config.Line
	seen := map[string]bool{}
	var collect func(id string)
	collect = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if id == "direct" {
			targets = append(targets, config.Line{ID: "direct", Name: "直连", Type: config.LineTypeDirect, Enabled: true})
			return
		}
		l := prof.FindLine(id)
		if l == nil {
			return
		}
		if l.IsGroup() {
			for _, child := range l.GroupMembers {
				collect(child)
			}
			return
		}
		targets = append(targets, *l)
	}
	for _, id := range lineIDs {
		collect(id)
	}

	for _, t := range targets {
		prev := m.latencies[t.ID]
		prev.Testing = true
		prev.Error = ""
		m.latencies[t.ID] = prev
	}
	activeBox := m.activeBox
	tagMap := m.activeLineOutbounds
	isConnected := m.phase == PhaseConnected
	m.mu.Unlock()
	m.emitUpdate()

	go func() {
		sem := make(chan struct{}, 3)
		var wg sync.WaitGroup
		for _, line := range targets {
			line := line
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case <-ctx.Done():
					m.clearTestingFlag(line.ID)
					return
				case sem <- struct{}{}:
				}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					m.clearTestingFlag(line.ID)
					return
				}

				var ms *int
				var errStr string

				if isConnected && activeBox != nil && tagMap[line.ID] != "" {
					val, err := activeBox.TestOutbound(tagMap[line.ID], "", 5000)
					if err != nil {
						errStr = err.Error()
					} else {
						ms = &val
					}
				} else {
					rawLine, _ := json.Marshal(line)
					resJSON, err := libbox.ProbeStandaloneLineLatency(string(rawLine), 5000)
					if err != nil {
						errStr = err.Error()
					} else {
						var parsed struct {
							Milliseconds *int `json:"milliseconds"`
						}
						if json.Unmarshal([]byte(resJSON), &parsed) == nil && parsed.Milliseconds != nil {
							ms = parsed.Milliseconds
						}
					}
				}

				m.mu.Lock()
				m.latencies[line.ID] = LatencyRecord{
					Milliseconds: ms,
					Error:        errStr,
					Testing:      false,
					ObservedAt:   time.Now().UnixMilli(),
				}
				m.mu.Unlock()
				m.emitUpdate()
			}()
		}
		wg.Wait()
	}()
	return nil
}

func (m *Manager) clearTestingFlag(lineID string) {
	m.mu.Lock()
	rec := m.latencies[lineID]
	rec.Testing = false
	m.latencies[lineID] = rec
	m.mu.Unlock()
	m.emitUpdate()
}

func (m *Manager) StopLatencyTests() {
	m.mu.Lock()
	if m.latencyCancel != nil {
		m.latencyCancel()
		m.latencyCancel = nil
	}
	for k, v := range m.latencies {
		if v.Testing {
			v.Testing = false
			m.latencies[k] = v
		}
	}
	m.mu.Unlock()
	m.emitUpdate()
}

// --- Cloud Host ("添加我的云主机…") SSH Deployment Wizard ---

type cloudHostListenerAdapter struct {
	mgr *Manager
}

func (l *cloudHostListenerAdapter) OnEvent(eventJSON string) {
	if l.mgr.onCloudHostEvent == nil {
		return
	}
	var payload map[string]any
	if json.Unmarshal([]byte(eventJSON), &payload) == nil {
		l.mgr.onCloudHostEvent(payload)
	}
}

func unwrapCloudHostEnvelope(raw string) (json.RawMessage, error) {
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}
	if !env.OK {
		if env.Error != nil && env.Error.Message != "" {
			return nil, errors.New(env.Error.Message)
		}
		return nil, errors.New("云主机操作失败")
	}
	return env.Data, nil
}

func (m *Manager) CloudHostScanKey(targetJSON string) (json.RawMessage, error) {
	return unwrapCloudHostEnvelope(m.cloudHost.ScanHostKey(targetJSON))
}

func (m *Manager) CloudHostInspect(targetJSON string) (json.RawMessage, error) {
	return unwrapCloudHostEnvelope(m.cloudHost.ConnectAndInspect(targetJSON))
}

func (m *Manager) CloudHostPlan(optionsJSON string) (json.RawMessage, error) {
	if strings.TrimSpace(optionsJSON) == "" {
		optionsJSON = "{}"
	}
	return unwrapCloudHostEnvelope(m.cloudHost.PlanWithOptions(optionsJSON))
}

func (m *Manager) CloudHostDeployAndSave(protocol string, port int, serverName, lineName string) (json.RawMessage, error) {
	reqRaw, _ := json.Marshal(map[string]any{
		"protocol":    protocol,
		"port":        port,
		"server_name": serverName,
	})
	deployData, err := unwrapCloudHostEnvelope(m.cloudHost.DeployWithOptions(string(reqRaw), &cloudHostListenerAdapter{mgr: m}))
	if err != nil {
		return nil, err
	}
	var deployed struct {
		Protocol    string `json:"protocol"`
		Server      string `json:"server"`
		Port        int    `json:"port"`
		Method      string `json:"method"`
		Password    string `json:"password"`
		UUID        string `json:"uuid"`
		Flow        string `json:"flow"`
		ServerName  string `json:"server_name"`
		PublicKey   string `json:"public_key"`
		ShortID     string `json:"short_id"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(deployData, &deployed); err != nil {
		return nil, err
	}

	var candidateLine config.Line
	firewallHint := "TCP/UDP"
	if deployed.Protocol == "vless-reality" {
		firewallHint = "TCP"
		fp := deployed.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		candidateLine = config.Line{
			Name:                   strings.TrimSpace(lineName),
			Type:                   config.LineTypeVLESS,
			Enabled:                true,
			VLESSServer:            deployed.Server,
			VLESSPort:              deployed.Port,
			VLESSUUID:              deployed.UUID,
			VLESSFlow:              deployed.Flow,
			VLESSSNI:               deployed.ServerName,
			VLESSClientFingerprint: fp,
			VLESSRealityPublicKey:  deployed.PublicKey,
			VLESSRealityShortID:    deployed.ShortID,
			UDP:                    true,
		}
	} else {
		candidateLine = config.Line{
			Name:     strings.TrimSpace(lineName),
			Type:     config.LineTypeShadowsocks,
			Enabled:  true,
			SSServer: deployed.Server,
			SSPort:   deployed.Port,
			SSMethod: deployed.Method,
			SSPass:   deployed.Password,
		}
	}
	if candidateLine.Name == "" {
		candidateLine.Name = "云主机 " + deployed.Server
	}

	lineRaw, _ := json.Marshal(candidateLine)
	probeRes, err := unwrapCloudHostEnvelope(libbox.ProbeStandaloneLineThroughput(string(lineRaw), 15000))
	if err != nil {
		return nil, fmt.Errorf("部署完成但连通验证未通过（请检查云厂商安全组是否放行 %s %d 端口）: %w", firewallHint, deployed.Port, err)
	}

	if err := m.SaveLine(candidateLine); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]any{
		"deployed":   deployed,
		"throughput": probeRes,
	})
	return out, nil
}

func (m *Manager) CloudHostCancel() {
	m.cloudHost.Cancel()
}

func (m *Manager) CloudHostClose() {
	m.cloudHost.Close()
}

func ExportDiagnosticReport(snap AppSnapshot) (string, error) {
	// Scrub any sensitive fields — Snapshot already contains only safe display models,
	// export diagnostic summary to user temp or clipboard string
	diag := map[string]any{
		"version":             snap.Version,
		"phase":               snap.Phase,
		"active_ingress_mode": snap.ActiveIngressMode,
		"active_socks_port":   snap.ActiveSOCKSPort,
		"system_facts":        snap.SystemFacts,
		"tasks":               snap.Tasks,
		"failure":             snap.Failure,
		"switch_failure":      snap.SwitchFailure,
		"timestamp":           time.Now().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(diag, "", "  ")
	if err != nil {
		return "", err
	}
	_ = filepath.Join(os.TempDir(), "xdial-diag.json")
	return string(raw), nil
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
