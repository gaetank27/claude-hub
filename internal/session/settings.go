package session

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// runtimeSettings are the launch/supervision parameters editable at runtime from
// the Configuration UI. They overlay the env-derived Config defaults. Immutable
// infrastructure (data dir, binaries, auth) is NOT here — it stays in Config.
type runtimeSettings struct {
	PermissionMode   string
	Model            string
	MemoryMax        string
	CPUQuota         string
	MaxTurns         int
	Inactivity       time.Duration
	OutputTruncation bool
}

func runtimeSettingsFromConfig(c Config) runtimeSettings {
	return runtimeSettings{
		PermissionMode: c.PermissionMode,
		Model:          c.Model,
		MemoryMax:      c.MemoryMax,
		CPUQuota:       c.CPUQuota,
		MaxTurns:       c.MaxTurns,
		Inactivity:     c.Inactivity,
	}
}

// settings returns a copy of the current runtime settings under lock.
func (m *Manager) settings() runtimeSettings {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	return m.rt
}

// Settings keys persisted in the store.
const (
	setPermissionMode   = "permission_mode"
	setModel            = "model"
	setMemoryMax        = "memory_max"
	setCPUQuota         = "cpu_quota"
	setMaxTurns         = "max_turns"
	setInactivitySec    = "inactivity_seconds"
	setOutputTruncation = "output_truncation"
)

// LoadSettings overlays any persisted settings onto the env defaults. Called once
// at boot, after NewManager. Unknown/blank keys keep the default.
func (m *Manager) LoadSettings() error {
	kv, err := m.db.GetSettings()
	if err != nil {
		return err
	}
	m.setMu.Lock()
	defer m.setMu.Unlock()
	if v, ok := kv[setPermissionMode]; ok && validPermissionMode(v) {
		m.rt.PermissionMode = v
	}
	if v, ok := kv[setModel]; ok && v != "" {
		m.rt.Model = v
	}
	if v, ok := kv[setMemoryMax]; ok {
		m.rt.MemoryMax = v
	}
	if v, ok := kv[setCPUQuota]; ok {
		m.rt.CPUQuota = v
	}
	if v, ok := kv[setMaxTurns]; ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			m.rt.MaxTurns = n
		}
	}
	if v, ok := kv[setInactivitySec]; ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			m.rt.Inactivity = time.Duration(n) * time.Second
		}
	}
	if v, ok := kv[setOutputTruncation]; ok {
		m.rt.OutputTruncation = v == "1"
	}
	return nil
}

// SettingsView is the effective settings reported to / accepted from the UI.
type SettingsView struct {
	PermissionMode    string `json:"permissionMode"`
	Model             string `json:"model"`
	MemoryMax         string `json:"memoryMax"`
	CPUQuota          string `json:"cpuQuota"`
	MaxTurns          int    `json:"maxTurns"`
	InactivitySeconds int    `json:"inactivitySeconds"`
	OutputTruncation  bool   `json:"outputTruncation"`
	// Network / domain settings (stored in app_meta, not the settings table).
	PublicBaseURL    string `json:"publicBaseUrl"`    // e.g. https://hub.example.com
	DnsServerIpMode  string `json:"dnsServerIpMode"`  // "local" | "public" | "custom"
	DnsServerIP      string `json:"dnsServerIp"`      // used only when mode = "custom"
	// Hands-free voice mode (live conversation) in the UI; off by default because
	// it needs a server fast enough for local STT. Stored in app_meta.
	HandsFreeVoice bool `json:"handsFreeVoice"`
}

// Settings returns the current effective runtime settings.
func (m *Manager) Settings() SettingsView {
	s := m.settings()
	// Network settings come from app_meta (infrastructure, not runtime tunables).
	publicBaseURL, _ := m.db.GetAppMeta("public_base_url")
	dnsServerIpMode, _ := m.db.GetAppMeta("dns_server_ip_mode")
	dnsServerIP, _ := m.db.GetAppMeta("dns_server_ip")
	handsFree, _ := m.db.GetAppMeta("voice_handsfree")
	return SettingsView{
		PermissionMode:    s.PermissionMode,
		Model:             s.Model,
		MemoryMax:         s.MemoryMax,
		CPUQuota:          s.CPUQuota,
		MaxTurns:          s.MaxTurns,
		InactivitySeconds: int(s.Inactivity / time.Second),
		OutputTruncation:  s.OutputTruncation,
		PublicBaseURL:     publicBaseURL,
		DnsServerIpMode:   dnsServerIpMode,
		DnsServerIP:       dnsServerIP,
		HandsFreeVoice:    handsFree == "1",
	}
}

// GetPublicBaseURL returns the current public base URL (dynamic, from app_meta if set,
// else from the startup config). Safe for concurrent use.
func (m *Manager) GetPublicBaseURL() string {
	if v, err := m.db.GetAppMeta("public_base_url"); err == nil && v != "" {
		return v
	}
	return m.cfg.PublicBaseURL
}

// UpdateSettings validates and applies new runtime settings: it persists them and
// updates the in-memory overlay. New sessions/supervisors pick them up; in-flight
// ones are unaffected. An invalid permission mode or non-positive bound is
// rejected.
func (m *Manager) UpdateSettings(v SettingsView) error {
	if !validPermissionMode(v.PermissionMode) {
		return errInvalidSetting("permissionMode")
	}
	if v.MaxTurns <= 0 || v.InactivitySeconds <= 0 {
		return errInvalidSetting("maxTurns/inactivitySeconds must be positive")
	}
	truncVal := "0"
	if v.OutputTruncation {
		truncVal = "1"
	}
	persist := map[string]string{
		setPermissionMode:   v.PermissionMode,
		setModel:            v.Model,
		setMemoryMax:        v.MemoryMax,
		setCPUQuota:         v.CPUQuota,
		setMaxTurns:         strconv.Itoa(v.MaxTurns),
		setInactivitySec:    strconv.Itoa(v.InactivitySeconds),
		setOutputTruncation: truncVal,
	}
	for k, val := range persist {
		if err := m.db.SetSetting(k, val); err != nil {
			return err
		}
	}
	// Network settings (publicBaseURL, dnsServerIp*) live in app_meta, not settings.
	if err := m.db.SetAppMeta("public_base_url", v.PublicBaseURL); err != nil {
		return err
	}
	switch v.DnsServerIpMode {
	case "", "local", "public", "custom":
	default:
		return fmt.Errorf("invalid dnsServerIpMode %q", v.DnsServerIpMode)
	}
	if v.DnsServerIP != "" && net.ParseIP(v.DnsServerIP) == nil {
		return fmt.Errorf("invalid dnsServerIp %q", v.DnsServerIP)
	}
	if err := m.db.SetAppMeta("dns_server_ip_mode", v.DnsServerIpMode); err != nil {
		return err
	}
	if err := m.db.SetAppMeta("dns_server_ip", v.DnsServerIP); err != nil {
		return err
	}
	handsFree := "0"
	if v.HandsFreeVoice {
		handsFree = "1"
	}
	if err := m.db.SetAppMeta("voice_handsfree", handsFree); err != nil {
		return err
	}
	m.setMu.Lock()
	m.rt = runtimeSettings{
		PermissionMode:   v.PermissionMode,
		Model:            v.Model,
		MemoryMax:        v.MemoryMax,
		CPUQuota:         v.CPUQuota,
		MaxTurns:         v.MaxTurns,
		Inactivity:       time.Duration(v.InactivitySeconds) * time.Second,
		OutputTruncation: v.OutputTruncation,
	}
	m.setMu.Unlock()
	return nil
}

type errInvalidSetting string

func (e errInvalidSetting) Error() string { return "invalid setting: " + string(e) }
