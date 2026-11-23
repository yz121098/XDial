package netwin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKeyPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runRegistryKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runRegistryValueName    = "XDial"

	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37

	defaultProxyBypass = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"
)

var (
	modwininet            = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption = modwininet.NewProc("InternetSetOptionW")
)

type proxyJournal struct {
	Owned             bool   `json:"owned"`
	TargetServer      string `json:"target_server"`
	PrevProxyEnable   uint64 `json:"prev_proxy_enable"`
	HadProxyEnable    bool   `json:"had_proxy_enable"`
	PrevProxyServer   string `json:"prev_proxy_server"`
	HadProxyServer    bool   `json:"had_proxy_server"`
	PrevProxyOverride string `json:"prev_proxy_override"`
	HadProxyOverride  bool   `json:"had_proxy_override"`
}

func journalPath(rootDir string) string {
	return filepath.Join(rootDir, "proxy-recovery.json")
}

func notifyWinInetRefresh() {
	if err := modwininet.Load(); err == nil {
		procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
		procInternetSetOption.Call(0, internetOptionRefresh, 0, 0)
	}
}

// EnableSystemProxy records the user's pre-existing WinINET proxy settings to disk
// before enabling 127.0.0.1:<port>, ensuring crash recovery never loses prior user settings.
func EnableSystemProxy(rootDir string, port int) error {
	target := fmt.Sprintf("127.0.0.1:%d", port)
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings registry: %w", err)
	}
	defer k.Close()

	// Only capture a new baseline if we don't already own an active journal
	jPath := journalPath(rootDir)
	if _, statErr := os.Stat(jPath); errors.Is(statErr, os.ErrNotExist) {
		var j proxyJournal
		j.Owned = true
		j.TargetServer = target
		if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
			j.PrevProxyEnable = v
			j.HadProxyEnable = true
		}
		if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
			j.PrevProxyServer = v
			j.HadProxyServer = true
		}
		if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
			j.PrevProxyOverride = v
			j.HadProxyOverride = true
		}
		if raw, mErr := json.MarshalIndent(j, "", "  "); mErr == nil {
			_ = os.WriteFile(jPath, raw, 0o600)
		}
	}

	if err := k.SetStringValue("ProxyServer", target); err != nil {
		return fmt.Errorf("set ProxyServer: %w", err)
	}
	if err := k.SetStringValue("ProxyOverride", defaultProxyBypass); err != nil {
		return fmt.Errorf("set ProxyOverride: %w", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("set ProxyEnable: %w", err)
	}
	notifyWinInetRefresh()
	return nil
}

// DisableSystemProxy restores the user's pre-existing WinINET proxy settings if owned by XDial.
func DisableSystemProxy(rootDir string) error {
	jPath := journalPath(rootDir)
	raw, err := os.ReadFile(jPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j proxyJournal
	if err := json.Unmarshal(raw, &j); err != nil || !j.Owned {
		_ = os.Remove(jPath)
		return nil
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings registry: %w", err)
	}
	defer k.Close()

	// Only revert if current ProxyServer still points to XDial's loopback endpoint
	curServer, _, _ := k.GetStringValue("ProxyServer")
	if curServer == j.TargetServer || curServer == "" {
		if j.HadProxyEnable {
			_ = k.SetDWordValue("ProxyEnable", uint32(j.PrevProxyEnable))
		} else {
			_ = k.SetDWordValue("ProxyEnable", 0)
		}
		if j.HadProxyServer {
			_ = k.SetStringValue("ProxyServer", j.PrevProxyServer)
		} else {
			_ = k.DeleteValue("ProxyServer")
		}
		if j.HadProxyOverride {
			_ = k.SetStringValue("ProxyOverride", j.PrevProxyOverride)
		}
		notifyWinInetRefresh()
	}
	_ = os.Remove(jPath)
	return nil
}

func IsLaunchAtLoginEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	val, _, err := k.GetStringValue(runRegistryValueName)
	return err == nil && val != ""
}

func SetLaunchAtLogin(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enabled {
		err := k.DeleteValue(runRegistryValueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runRegistryValueName, fmt.Sprintf(`"%s" --minimized`, exe))
}
