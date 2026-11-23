package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kafeifei/xdial/core/config"
	"github.com/kafeifei/xdial/windows/internal/runtime"
	"github.com/kafeifei/xdial/windows/internal/store"
	"github.com/kafeifei/xdial/windows/internal/trayicon"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var embeddedDist embed.FS

var version = "v0.9.8-windows-dev"

type rpcRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

type uiBridge struct {
	mgr         *runtime.Manager
	app         *application.App
	tray        *application.SystemTray
	flyoutWin   *application.WebviewWindow
	settingsWin *application.WebviewWindow
	quitting    atomic.Bool

	trayMu      sync.Mutex
	lastState   trayicon.State
	lastBadge   trayicon.Badge
	trayInited  bool
}

func formatCompactRate(bps *float64) string {
	if bps == nil || math.IsNaN(*bps) || math.IsInf(*bps, 0) || *bps < 0 {
		return "—"
	}
	v := *bps
	if v == 0 {
		return "0 B/s"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	u := 0
	for v >= 1000 && u < len(units)-1 {
		v /= 1000
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

func traySpecFromSnapshot(snap runtime.AppSnapshot) (trayicon.State, trayicon.Badge) {
	st := trayicon.StateDisconnected
	switch snap.Phase {
	case runtime.PhaseConnecting, runtime.PhaseSwitching, runtime.PhaseDisconnecting:
		st = trayicon.StateConnecting
	case runtime.PhaseConnected:
		if snap.IsProxyOnly {
			st = trayicon.StateProxyOnly
		} else {
			st = trayicon.StateConnected
		}
	default:
		st = trayicon.StateDisconnected
	}

	badge := trayicon.BadgeNone
	if snap.Phase == runtime.PhaseFailed || snap.SwitchFailure != nil {
		badge = trayicon.BadgeError
	}
	return st, badge
}

func (b *uiBridge) updateTrayVisuals(snap runtime.AppSnapshot, forceFrame int) {
	if b.tray == nil {
		return
	}
	st, badge := traySpecFromSnapshot(snap)

	b.trayMu.Lock()
	changed := !b.trayInited || b.lastState != st || b.lastBadge != badge || forceFrame >= 0
	b.lastState = st
	b.lastBadge = badge
	b.trayInited = true
	b.trayMu.Unlock()

	if changed {
		frame := 0
		if forceFrame >= 0 {
			frame = forceFrame
		}
		lightPNG := trayicon.RenderPNG(trayicon.Spec{
			State:       st,
			Badge:       badge,
			PulseFrame:  frame,
			DarkTaskbar: false,
		})
		darkPNG := trayicon.RenderPNG(trayicon.Spec{
			State:       st,
			Badge:       badge,
			PulseFrame:  frame,
			DarkTaskbar: true,
		})
		b.tray.SetIcon(lightPNG)
		b.tray.SetDarkModeIcon(darkPNG)
	}

	// Build rich hover tooltip
	scName := ""
	for _, sc := range snap.Editor.Scenarios {
		if sc.ID == snap.Editor.ActiveScenarioID {
			scName = sc.Name
			break
		}
	}
	var tip string
	switch snap.Phase {
	case runtime.PhaseConnected:
		modeTag := ""
		if snap.IsProxyOnly {
			modeTag = "（仅 SOCKS5）"
		}
		if snap.Preferences.ShowTraySpeed {
			tip = fmt.Sprintf("XDial · 已连接%s · %s\n↓ %s  ↑ %s", modeTag, scName, formatCompactRate(snap.DownloadBps), formatCompactRate(snap.UploadBps))
		} else {
			tip = fmt.Sprintf("XDial · 已连接%s · %s", modeTag, scName)
		}
	case runtime.PhaseConnecting:
		tip = fmt.Sprintf("XDial · 正在连接 %s…", snap.TargetScenarioName)
	case runtime.PhaseSwitching:
		tip = fmt.Sprintf("XDial · 正在切换到 %s…", snap.TargetScenarioName)
	case runtime.PhaseFailed:
		tip = "XDial · 连接失败"
	default:
		if scName != "" {
			tip = fmt.Sprintf("XDial · 未连接（%s）", scName)
		} else {
			tip = "XDial · 未连接"
		}
	}
	b.tray.SetTooltip(tip)
}

func (b *uiBridge) pushSnapshotToWindow(win *application.WebviewWindow, snap runtime.AppSnapshot) {
	if win == nil {
		return
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	win.ExecJS(fmt.Sprintf("window.__xdialPush && window.__xdialPush(%s);", string(raw)))
}

func (b *uiBridge) broadcastSnapshot(snap runtime.AppSnapshot) {
	b.updateTrayVisuals(snap, -1)
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	js := fmt.Sprintf("window.__xdialPush && window.__xdialPush(%s);", string(raw))
	// Always push regardless of IsVisible() so hidden flyout/settings WebViews never freeze on stale state.
	if b.flyoutWin != nil {
		b.flyoutWin.ExecJS(js)
	}
	if b.settingsWin != nil {
		b.settingsWin.ExecJS(js)
	}
}

func (b *uiBridge) broadcastCloudHostEvent(ev map[string]any) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	js := fmt.Sprintf("window.__xdialCloudHostEvent && window.__xdialCloudHostEvent(%s);", string(raw))
	if b.settingsWin != nil {
		b.settingsWin.ExecJS(js)
	}
}

func (b *uiBridge) runTrayPulseLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			b.trayMu.Lock()
			animating := b.lastState == trayicon.StateConnecting && b.lastBadge != trayicon.BadgeError
			b.trayMu.Unlock()
			if animating {
				frame = (frame + 1) % 12
				b.updateTrayVisuals(b.mgr.GetSnapshot(), frame)
			}
		}
	}
}

func (b *uiBridge) openSettingsWindow(targetSection string) {
	if b.settingsWin == nil {
		return
	}
	b.settingsWin.Show().Focus()
	if targetSection != "" {
		quoted, _ := json.Marshal(targetSection)
		b.settingsWin.ExecJS(fmt.Sprintf("window.__xdialNavigateSettings && window.__xdialNavigateSettings(%s);", string(quoted)))
	}
}

func (b *uiBridge) quit() {
	b.quitting.Store(true)
	if b.app != nil {
		b.app.Quit()
	}
}

func (b *uiBridge) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/snapshot":
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			rw.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(rw).Encode(b.mgr.GetSnapshot())
			return

		case "/api/rpc":
			if req.Method != http.MethodPost {
				http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, 4*1024*1024))
			if err != nil {
				writeRPCError(rw, err)
				return
			}
			var call rpcRequest
			if err := json.Unmarshal(body, &call); err != nil {
				writeRPCError(rw, err)
				return
			}
			res, callErr := b.dispatchRPC(call.Method, call.Params)
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			if callErr != nil {
				_ = json.NewEncoder(rw).Encode(rpcResponse{OK: false, Error: callErr.Error()})
				return
			}
			_ = json.NewEncoder(rw).Encode(rpcResponse{OK: true, Data: res})
			return
		}
		next.ServeHTTP(rw, req)
	})
}

func writeRPCError(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(rpcResponse{OK: false, Error: err.Error()})
}

func (b *uiBridge) dispatchRPC(method string, params json.RawMessage) (any, error) {
	switch method {
	case "getSnapshot":
		return b.mgr.GetSnapshot(), nil
	case "connect":
		return nil, b.mgr.Connect()
	case "reconnect":
		return nil, b.mgr.Reconnect()
	case "disconnect":
		return nil, b.mgr.Disconnect()
	case "cancelConnect":
		return nil, b.mgr.CancelConnect()
	case "dismissSwitchFailure":
		b.mgr.DismissSwitchFailure()
		return nil, nil

	case "dismissOnboarding":
		return nil, b.mgr.DismissOnboarding()

	case "signInAndConnect":
		var p struct {
			LineID   string `json:"line_id"`
			Server   string `json:"server"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.SignInAndConnect(p.LineID, p.Server, p.Username, p.Password)

	case "selectScenario":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.SelectScenario(p.ID)

	case "setAutomaticScenario":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.SetAutomaticScenario(p.Enabled)

	case "savePreferences":
		var prefs store.Preferences
		if err := json.Unmarshal(params, &prefs); err != nil {
			return nil, err
		}
		return nil, b.mgr.SavePreferences(prefs)

	// Window controls
	case "openSettings":
		var p struct {
			Section string `json:"section"`
		}
		_ = json.Unmarshal(params, &p)
		b.openSettingsWindow(p.Section)
		return nil, nil

	case "hideFlyout":
		if b.tray != nil {
			b.tray.HideWindow()
		}
		return nil, nil

	case "setFlyoutHeight":
		var p struct {
			Height int `json:"height"`
		}
		if err := json.Unmarshal(params, &p); err == nil && b.flyoutWin != nil {
			h := p.Height
			if h < 310 {
				h = 310
			}
			if h > 720 {
				h = 720
			}
			b.flyoutWin.SetSize(360, h)
			if b.flyoutWin.IsVisible() && b.tray != nil {
				_ = b.tray.PositionWindow(b.flyoutWin, 8)
			}
		}
		return nil, nil

	case "quitApp":
		go func() {
			time.Sleep(50 * time.Millisecond)
			b.quit()
		}()
		return nil, nil

	// Profile Library operations
	case "selectProfileSource":
		var p struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.SelectProfileSource(p.SourceID)

	case "createBlankProfile":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.CreateBlankProfile(p.Name)

	case "importProfileContent":
		var p struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.ImportProfileContent(p.Name, p.Content)

	case "importSubscriptionURL":
		var p struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.ImportSubscriptionURL(p.Name, p.URL)

	case "refreshProfileSource":
		var p struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.RefreshProfileSource(p.SourceID)

	case "renameProfileSource":
		var p struct {
			SourceID string `json:"source_id"`
			Name     string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.RenameProfileSource(p.SourceID, p.Name)

	case "exportProfileSource":
		var p struct {
			SourceID string `json:"source_id"`
		}
		_ = json.Unmarshal(params, &p)
		return b.mgr.ExportProfileSource(p.SourceID)

	case "deleteProfileSource":
		var p struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DeleteProfileSource(p.SourceID)

	case "resetToDefaults":
		return nil, b.mgr.ResetToDefaults()

	// Lines, Groups, Rules, Scenarios CRUD
	case "saveLine":
		var line config.Line
		if err := json.Unmarshal(params, &line); err != nil {
			return nil, err
		}
		return nil, b.mgr.SaveLine(line)

	case "deleteLine":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DeleteLine(p.ID)

	case "reorderLines":
		var p struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.ReorderLines(p.IDs)

	case "saveLineGroup":
		var grp config.Line
		if err := json.Unmarshal(params, &grp); err != nil {
			return nil, err
		}
		return nil, b.mgr.SaveLineGroup(grp)

	case "deleteLineGroup":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DeleteLineGroup(p.ID)

	case "saveRuleSet":
		var rule config.RuleSet
		if err := json.Unmarshal(params, &rule); err != nil {
			return nil, err
		}
		return nil, b.mgr.SaveRuleSet(rule)

	case "deleteRuleSet":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DeleteRuleSet(p.ID)

	case "reorderRuleSets":
		var p struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.ReorderRuleSets(p.IDs)

	case "saveScenario":
		var sc config.Scenario
		if err := json.Unmarshal(params, &sc); err != nil {
			return nil, err
		}
		return nil, b.mgr.SaveScenario(sc)

	case "duplicateScenario":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DuplicateScenario(p.ID)

	case "deleteScenario":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.DeleteScenario(p.ID)

	case "reorderScenarios":
		var p struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.ReorderScenarios(p.IDs)

	// Latency testing
	case "testBatchLatency":
		var p struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, b.mgr.TestBatchLatency(p.IDs)

	case "stopLatencyTests":
		b.mgr.StopLatencyTests()
		return nil, nil

	// Cloud Host SSH deployment wizard
	case "cloudHostScanKey":
		return b.mgr.CloudHostScanKey(string(params))
	case "cloudHostInspect":
		return b.mgr.CloudHostInspect(string(params))
	case "cloudHostPlan":
		var p struct {
			Port int `json:"port"`
		}
		_ = json.Unmarshal(params, &p)
		return b.mgr.CloudHostPlan(p.Port)
	case "cloudHostDeployAndSave":
		var p struct {
			Port int    `json:"port"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return b.mgr.CloudHostDeployAndSave(p.Port, p.Name)
	case "cloudHostCancel":
		b.mgr.CloudHostCancel()
		return nil, nil
	case "cloudHostClose":
		b.mgr.CloudHostClose()
		return nil, nil

	// Diagnostics export
	case "exportDiagnostics":
		return runtime.ExportDiagnosticReport(b.mgr.GetSnapshot())

	default:
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	}
}

func main() {
	minimized := false
	openSettingsOnStart := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--minimized":
			minimized = true
		case "--settings":
			openSettingsOnStart = true
		}
	}

	st, err := store.New("")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "XDial store initialization failed: %v\n", err)
		os.Exit(1)
	}

	bridge := &uiBridge{}
	mgr, err := runtime.NewManager(
		st,
		version,
		func(snap runtime.AppSnapshot) {
			bridge.broadcastSnapshot(snap)
		},
		func(ev map[string]any) {
			bridge.broadcastCloudHostEvent(ev)
		},
	)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "XDial runtime initialization failed: %v\n", err)
		os.Exit(1)
	}
	bridge.mgr = mgr

	distFS, err := fs.Sub(embeddedDist, "frontend/dist")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "embedded frontend dist missing: %v\n", err)
		os.Exit(1)
	}

	appIcon := trayicon.RenderPNG(trayicon.Spec{
		State:       trayicon.StateConnected,
		DarkTaskbar: false,
	})

	stopPulse := make(chan struct{})

	app := application.New(application.Options{
		Name:        "XDial",
		Description: "XDial Windows Network Scenario Router",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler:        application.AssetFileServerFS(distFS),
			Middleware:     bridge.middleware,
			DisableLogging: true,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.xd.xdial.windows.app",
			OnSecondInstanceLaunch: func(_ application.SecondInstanceData) {
				if bridge.tray != nil {
					bridge.tray.ShowWindow()
				}
			},
		},
		OnShutdown: func() {
			close(stopPulse)
			mgr.Shutdown()
		},
	})
	bridge.app = app

	// 1. Create 360px Tray Flyout Window (frameless, rounded on Win11, hidden on taskbar)
	flyoutWin := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          "flyout",
		Title:         "XDial",
		Width:         360,
		Height:        480,
		MinWidth:      360,
		MaxWidth:      360,
		MinHeight:     310,
		MaxHeight:     760,
		Frameless:     true,
		DisableResize: true,
		AlwaysOnTop:   true,
		Hidden:        true,
		URL:           "/?view=flyout",
		BackgroundColour: application.RGBA{
			Red: 27, Green: 30, Blue: 32, Alpha: 255,
		},
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})
	for _, evType := range []events.WindowEventType{
		events.Common.WindowShow,
		events.Common.WindowFocus,
		events.Common.WindowRestore,
	} {
		flyoutWin.OnWindowEvent(evType, func(_ *application.WindowEvent) {
			bridge.pushSnapshotToWindow(flyoutWin, mgr.GetSnapshot())
		})
	}
	bridge.flyoutWin = flyoutWin

	// 2. Create 810x565 Settings Window (matches DESIGN.md §5.1 exact geometry)
	settingsWin := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "settings",
		Title:     "XDial 设置",
		Width:     810,
		Height:    565,
		MinWidth:  680,
		MinHeight: 480,
		Hidden:    !openSettingsOnStart,
		URL:       "/?view=settings",
		BackgroundColour: application.RGBA{
			Red: 239, Green: 242, Blue: 243, Alpha: 255,
		},
		Windows: application.WindowsWindow{
			DisableMenu: true,
		},
	})
	settingsWin.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !bridge.quitting.Load() {
			event.Cancel()
			settingsWin.Hide()
		}
	})
	for _, evType := range []events.WindowEventType{
		events.Common.WindowShow,
		events.Common.WindowFocus,
		events.Common.WindowRestore,
	} {
		settingsWin.OnWindowEvent(evType, func(_ *application.WindowEvent) {
			bridge.pushSnapshotToWindow(settingsWin, mgr.GetSnapshot())
		})
	}
	bridge.settingsWin = settingsWin

	// 3. Create System Tray with dynamic Rotary Dial status icons + right-click menu
	tray := app.SystemTray.New()
	bridge.tray = tray
	bridge.updateTrayVisuals(mgr.GetSnapshot(), -1)

	trayMenu := app.NewMenu()
	trayMenu.Add("打开拨号盘").OnClick(func(_ *application.Context) {
		tray.ShowWindow()
	})
	trayMenu.Add("连接 / 断开当前场景").OnClick(func(_ *application.Context) {
		snap := mgr.GetSnapshot()
		if snap.Phase == runtime.PhaseConnected {
			_ = mgr.Disconnect()
		} else if snap.Phase == runtime.PhaseDisconnected || snap.Phase == runtime.PhaseFailed {
			_ = mgr.Connect()
		}
	})
	trayMenu.AddSeparator()
	trayMenu.Add("设置…").OnClick(func(_ *application.Context) {
		bridge.openSettingsWindow("")
	})
	trayMenu.AddSeparator()
	trayMenu.Add("退出 XDial").OnClick(func(_ *application.Context) {
		bridge.quit()
	})

	tray.SetMenu(trayMenu)
	tray.AttachWindow(flyoutWin).WindowOffset(8)

	go bridge.runTrayPulseLoop(stopPulse)

	if prefs := st.LoadPreferences(); prefs.ConnectOnLaunch {
		go func() {
			time.Sleep(400 * time.Millisecond)
			_ = mgr.Connect()
		}()
	}

	if !minimized && !openSettingsOnStart {
		go func() {
			time.Sleep(350 * time.Millisecond)
			tray.ShowWindow()
		}()
	}

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "XDial exited with error: %v\n", err)
		os.Exit(1)
	}
}
