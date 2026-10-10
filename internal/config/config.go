// Package config reads and writes launcher.json — the single source of truth
// for the launcher's persistent state.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Development builds can override this with -ldflags (see dev.bat).
var DefaultUpdateServer = "https://syntee.fun/api/public/updates"

func InstallDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}
	return filepath.Join(base, "Programs", "Syntee Helper")
}
func IsInstalled() bool {
	exe, err := os.Executable()
	return err == nil && filepath.Dir(exe) == InstallDir()
}

type Config struct {
	UpdateServer       string   `json:"update_server"`
	Channel            string   `json:"channel"`
	CurrentVersion     string   `json:"current_version"`
	PinnedVersion      *string  `json:"pinned_version,omitempty"`
	BadVersions        []string `json:"bad_versions"`
	CrashCount         int      `json:"crash_count"`
	MaxCrashRetries    int      `json:"max_crash_retries"`
	CheckTimeoutSec    int      `json:"check_timeout_sec"`
	DownloadTimeoutSec int      `json:"download_timeout_sec"`
	ClientExe          string   `json:"client_exe"`
	ReadyMarker        string   `json:"ready_marker"`
	DesktopShortcut    *bool    `json:"desktop_shortcut,omitempty"`
	StartMenuShortcut  *bool    `json:"start_menu_shortcut,omitempty"`
	AutoStart          *bool    `json:"auto_start,omitempty"`
	path               string
	mu                 sync.Mutex
	dirty              bool
	unknown            map[string]json.RawMessage
}

func Load() (*Config, error) {
	path := filepath.Join(InstallDir(), "launcher.json")
	c, err := loadAt(path)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !isParseError(err) {
		return nil, err
	}
	backup := path + ".bak"
	b, berr := loadAt(backup)
	if berr == nil {
		b.path = path
		return b, nil
	}
	if errors.Is(err, os.ErrNotExist) && errors.Is(berr, os.ErrNotExist) {
		c = &Config{path: path}
		c.applyDefaults()
		return c, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config and backup invalid: %v; %v", err, berr)
	}
	return nil, fmt.Errorf("load backup %s: %w", backup, berr)
}
func isParseError(err error) bool {
	return err != nil && len(err.Error()) >= 6 && err.Error()[:6] == "parse "
}
func loadAt(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	raw = trimBOM(raw)
	c := &Config{path: path}
	if err = json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var all map[string]json.RawMessage
	_ = json.Unmarshal(raw, &all)
	known := map[string]bool{"update_server": true, "channel": true, "current_version": true, "pinned_version": true, "bad_versions": true, "crash_count": true, "max_crash_retries": true, "check_timeout_sec": true, "download_timeout_sec": true, "client_exe": true, "ready_marker": true, "desktop_shortcut": true, "start_menu_shortcut": true, "auto_start": true}
	c.unknown = map[string]json.RawMessage{}
	for k, v := range all {
		if !known[k] {
			c.unknown[k] = v
		}
	}
	c.applyDefaults()
	return c, nil
}
func (c *Config) applyDefaults() {
	if c.UpdateServer == "" {
		c.UpdateServer = DefaultUpdateServer
	}
	if c.Channel == "" {
		c.Channel = "stable"
	}
	if c.MaxCrashRetries == 0 {
		c.MaxCrashRetries = 3
	}
	if c.CheckTimeoutSec == 0 {
		c.CheckTimeoutSec = 5
	}
	if c.ClientExe == "" {
		c.ClientExe = "Syntee Helper.exe"
	}
	if c.ReadyMarker == "" {
		c.ReadyMarker = ".ready"
	}
	if c.BadVersions == nil {
		c.BadVersions = []string{}
	}
}
func (c *Config) MarshalJSON() ([]byte, error) {
	type plain Config
	b, e := json.Marshal((*plain)(c))
	if e != nil {
		return nil, e
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for k, v := range c.unknown {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applyDefaults()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if c.path == "" {
		c.path = filepath.Join(InstallDir(), "launcher.json")
	}
	// Preserve only a parseable current file as the recovery copy.
	if old, e := os.ReadFile(c.path); e == nil {
		var valid json.RawMessage
		if json.Unmarshal(trimBOM(old), &valid) == nil {
			if e = AtomicWriteFile(c.path+".bak", old, 0o644); e != nil {
				return fmt.Errorf("backup config: %w", e)
			}
		}
	}
	if err = AtomicWriteFile(c.path, raw, 0o644); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	c.dirty = false
	return nil
}
func (c *Config) SetCurrentVersion(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CurrentVersion != v {
		c.CurrentVersion = v
		c.CrashCount = 0
		c.dirty = true
	}
}
func (c *Config) IncrementCrashCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.CrashCount++
	c.dirty = true
	return c.CrashCount
}
func (c *Config) ResetCrashCount() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CrashCount != 0 {
		c.CrashCount = 0
		c.dirty = true
	}
}
func (c *Config) AddBadVersion(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.BadVersions {
		if b == v {
			return
		}
	}
	c.BadVersions = append(c.BadVersions, v)
	c.dirty = true
}
func (c *Config) IsBad(v string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.BadVersions {
		if b == v {
			return true
		}
	}
	return false
}
func (c *Config) Dir() string             { return InstallDir() }
func (c *Config) CurrentDir() string      { return filepath.Join(c.Dir(), "current") }
func (c *Config) BackupsDir() string      { return filepath.Join(c.Dir(), "backups") }
func (c *Config) StagingDir() string      { return filepath.Join(c.Dir(), "staging") }
func (c *Config) ClientExePath() string   { return filepath.Join(c.CurrentDir(), c.ClientExe) }
func (c *Config) ReadyMarkerPath() string { return filepath.Join(c.CurrentDir(), c.ReadyMarker) }
func (c *Config) CrashMarkerPath() string { return filepath.Join(c.CurrentDir(), ".crash_marker") }
func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
