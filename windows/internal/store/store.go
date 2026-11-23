package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/kafeifei/xdial/core/config"
	"github.com/kafeifei/xdial/core/libbox"
	"golang.org/x/sys/windows"
)

type IngressMode string

const (
	IngressSystemProxy IngressMode = "system-proxy"
	IngressWintun      IngressMode = "wintun"
	IngressLocalSOCKS  IngressMode = "local-socks"
)

type Preferences struct {
	IngressMode          IngressMode `json:"ingress_mode"`
	AlsoExposeLocalSOCKS bool        `json:"also_expose_local_socks"`
	SOCKSPort            int         `json:"socks_port"`
	AutomaticScenario    bool        `json:"automatic_scenario"`
	LaunchAtLogin        bool        `json:"launch_at_login"`
	ConnectOnLaunch      bool        `json:"connect_on_launch"`
	NotifyDisconnect     bool        `json:"notify_disconnect"`
	NotifyScenarioSwitch bool        `json:"notify_scenario_switch"`
	NotifyNetworkChange  bool        `json:"notify_network_change"`
	Appearance           string      `json:"appearance"` // "system" | "light" | "dark"
	Language             string      `json:"language"`   // "zh-CN" | "en"
	ShowTraySpeed        bool        `json:"show_tray_speed"`
	ReceiveBeta          bool        `json:"receive_beta"`
	OnboardingDismissed  bool        `json:"onboarding_dismissed"`
}

func DefaultPreferences() Preferences {
	return Preferences{
		IngressMode:          IngressSystemProxy,
		AlsoExposeLocalSOCKS: true,
		SOCKSPort:            5181,
		AutomaticScenario:    false,
		LaunchAtLogin:        false,
		ConnectOnLaunch:      false,
		NotifyDisconnect:     false,
		NotifyScenarioSwitch: false,
		NotifyNetworkChange:  false,
		Appearance:           "system",
		Language:             "zh-CN",
		ShowTraySpeed:        true,
		ReceiveBeta:          false,
	}
}

type Store struct {
	mu      sync.Mutex
	rootDir string
}

func New(rootDir string) (*Store, error) {
	if rootDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user home directory: %w", err)
		}
		rootDir = filepath.Join(home, ".xdial", "windows")
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "runtime"), 0o700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	s := &Store{rootDir: rootDir}
	if _, err := s.LoadLibrary(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) RootDir() string {
	return s.rootDir
}

func (s *Store) RuntimeDir() string {
	return filepath.Join(s.rootDir, "runtime")
}

func (s *Store) libraryPath() string {
	return filepath.Join(s.rootDir, "profiles.dpapi")
}

func (s *Store) prefsPath() string {
	return filepath.Join(s.rootDir, "preferences.json")
}

// dpapiEncrypt encrypts user profile library bytes using Windows CryptProtectData
// bound to the current Windows logon identity.
func dpapiEncrypt(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty payload")
	}
	inBlob := windows.DataBlob{
		Size: uint32(len(plain)),
		Data: &plain[0],
	}
	var outBlob windows.DataBlob
	name, _ := windows.UTF16PtrFromString("XDial.ProfileLibrary.v3")
	if err := windows.CryptProtectData(&inBlob, name, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob); err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}

func dpapiDecrypt(cipher []byte) ([]byte, error) {
	if len(cipher) == 0 {
		return nil, errors.New("empty encrypted library")
	}
	inBlob := windows.DataBlob{
		Size: uint32(len(cipher)),
		Data: &cipher[0],
	}
	var outBlob windows.DataBlob
	if err := windows.CryptUnprotectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}

func atomicWriteFile(path string, data []byte) error {
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	tmp := fmt.Sprintf("%s.tmp.%s", path, hex.EncodeToString(suffix[:]))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	srcPtr, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dstPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := windows.MoveFileEx(srcPtr, dstPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func defaultSeedProfileJSON() string {
	seed := config.Profile{
		Lines: []config.Line{
			{
				ID:      "direct",
				Name:    "直连",
				Type:    config.LineTypeDirect,
				Enabled: true,
			},
		},
		RuleSets: []config.RuleSet{
			{
				ID:      "lan-private",
				Name:    "局域网私有地址",
				Type:    config.RuleSetTypeManual,
				Enabled: true,
				CIDRs:   []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			},
		},
		Scenarios: []config.Scenario{
			{
				ID:            "daily-direct",
				Name:          "日常直连",
				Icon:          "direct",
				DefaultLineID: "direct",
				Bindings: []config.RuleBinding{
					{
						RuleSetID: "lan-private",
						LineID:    "direct",
					},
				},
			},
		},
		ActiveScenarioID: "daily-direct",
	}
	raw, _ := json.Marshal(seed)
	return string(raw)
}

func discoverBundledSeedJSON() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "Seed.json"),
			filepath.Join(exeDir, "PrivateConfig", "Seed.json"),
		)
	}
	candidates = append(candidates, filepath.Join("PrivateConfig", "Seed.json"))

	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) == 0 {
			continue
		}
		var env struct {
			Profile map[string]json.RawMessage `json:"profile"`
		}
		if json.Unmarshal(raw, &env) == nil && len(env.Profile) > 0 {
			delete(env.Profile, "subscriptions")
			if out, mErr := json.Marshal(env.Profile); mErr == nil {
				return string(out)
			}
		}
		return string(raw)
	}
	return ""
}

func initialSeedProfileJSON() string {
	if bundled := discoverBundledSeedJSON(); bundled != "" {
		if _, err := libbox.NewProfileLibrary(bundled); err == nil {
			return bundled
		}
	}
	return defaultSeedProfileJSON()
}

// LoadLibrary reads and decrypts the Schema-3 ProfileLibrary JSON.
// If no library file exists yet, initializes a default library atomically.
// Consistent with ARCHITECTURE.md §2, corrupted/undecryptable libraries fail closed
// rather than silently resetting to an empty profile.
func (s *Store) LoadLibrary() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.libraryPath()
	cipher, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		libJSON, err := libbox.NewProfileLibrary(initialSeedProfileJSON())
		if err != nil {
			return "", fmt.Errorf("create initial profile library: %w", err)
		}
		enc, err := dpapiEncrypt([]byte(libJSON))
		if err != nil {
			return "", err
		}
		if err := atomicWriteFile(path, enc); err != nil {
			return "", err
		}
		return libJSON, nil
	}
	if err != nil {
		return "", fmt.Errorf("read profile library: %w", err)
	}
	plain, err := dpapiDecrypt(cipher)
	if err != nil {
		return "", fmt.Errorf("无法解密本地配置库（拒绝覆盖原文件）: %w", err)
	}
	// Validate schema-3 integrity via Go control plane
	if _, err := libbox.ProfileLibraryRuntime(string(plain)); err != nil {
		return "", fmt.Errorf("配置库校验失败（拒绝覆盖原文件）: %w", err)
	}
	return string(plain), nil
}

func (s *Store) SaveLibrary(libraryJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := libbox.ProfileLibraryRuntime(libraryJSON); err != nil {
		return fmt.Errorf("invalid profile library: %w", err)
	}
	enc, err := dpapiEncrypt([]byte(libraryJSON))
	if err != nil {
		return err
	}
	return atomicWriteFile(s.libraryPath(), enc)
}

func (s *Store) ResetToDefaults() (string, error) {
	libJSON, err := libbox.NewProfileLibrary(initialSeedProfileJSON())
	if err != nil {
		return "", err
	}
	if err := s.SaveLibrary(libJSON); err != nil {
		return "", err
	}
	return libJSON, nil
}

func (s *Store) LoadPreferences() Preferences {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefs := DefaultPreferences()
	raw, err := os.ReadFile(s.prefsPath())
	if err != nil {
		return prefs
	}
	_ = json.Unmarshal(raw, &prefs)
	if prefs.SOCKSPort < 1024 || prefs.SOCKSPort > 65535 {
		prefs.SOCKSPort = 5181
	}
	switch prefs.IngressMode {
	case IngressSystemProxy, IngressWintun, IngressLocalSOCKS:
	default:
		prefs.IngressMode = IngressSystemProxy
	}
	if prefs.Appearance != "light" && prefs.Appearance != "dark" {
		prefs.Appearance = "system"
	}
	if prefs.Language != "en" {
		prefs.Language = "zh-CN"
	}
	return prefs
}

func (s *Store) SavePreferences(prefs Preferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if prefs.SOCKSPort < 1024 || prefs.SOCKSPort > 65535 {
		return fmt.Errorf("SOCKS5 端口必须在 1024–65535 之间")
	}
	raw, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.prefsPath(), raw)
}
