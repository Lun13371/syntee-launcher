// Package config reads and writes launcher.json — the single source of truth
// for the launcher's persistent state. The file lives next to the launcher
// binary so that the whole install directory is self-contained and a clean
// uninstall is just "delete the folder".
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config mirrors launcher.json. All fields have json tags so the file is
// human-editable. Defaults are applied by Load when the file is missing or
// when individual fields are zero-valued.
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

	path   string
	mu     sync.Mutex
	dirty  bool
}

// Load reads launcher.json from the directory containing the running binary.
// If the file is absent, a Config with sensible defaults is returned (and can
// be persisted with Save). Missing fields in a partial file are filled in
// with the same defaults.
func Load() (*Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("determine executable path: %w", err)
	}
	dir := filepath.Dir(exe)
	path := filepath.Join(dir, "launcher.json")

	c := &Config{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		// Strip a UTF-8 BOM if present — Windows tools (Notepad, PowerShell
		// Out-File) sometimes write one, and json.Unmarshal rejects it with
		// "invalid character 'ï' looking for beginning of value".
		raw = trimBOM(raw)
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		c.applyDefaults()
	case os.IsNotExist(err):
		// First run — no config yet. Return defaults without writing; the
		// installer (NSIS) is responsible for seeding the initial file.
		c.applyDefaults()
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return c, nil
}

func (c *Config) applyDefaults() {
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
		c.ClientExe = "SynteeHelper.exe"
	}
	if c.ReadyMarker == "" {
		c.ReadyMarker = ".ready"
	}
	if c.BadVersions == nil {
		c.BadVersions = []string{}
	}
}

// Save writes the config atomically (temp file + rename) so a crash mid-write
// never corrupts the file the launcher needs to boot.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.applyDefaults()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, c.path, err)
	}
	c.dirty = false
	return nil
}

// SetCurrentVersion updates the running version and marks the config dirty so
// the caller knows to Save. CrashCount is reset whenever a new version is
// recorded — a fresh download starts the crash counter from zero.
func (c *Config) SetCurrentVersion(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CurrentVersion == v {
		return
	}
	c.CurrentVersion = v
	c.CrashCount = 0
	c.dirty = true
}

// IncrementCrashCount bumps the crash counter and returns the new value.
func (c *Config) IncrementCrashCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.CrashCount++
	c.dirty = true
	return c.CrashCount
}

// ResetCrashCount zeros the counter (called after the client signals ready).
func (c *Config) ResetCrashCount() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CrashCount != 0 {
		c.CrashCount = 0
		c.dirty = true
	}
}

// AddBadVersion records a version that crashed too many times so the launcher
// never tries to install it again automatically.
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

// IsBad reports whether v is in the bad-versions list.
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

// Dir returns the directory containing launcher.json (i.e. the install root).
func (c *Config) Dir() string {
	return filepath.Dir(c.path)
}

// CurrentDir is the "current/" subfolder holding the active client build.
func (c *Config) CurrentDir() string {
	return filepath.Join(c.Dir(), "current")
}

// BackupsDir is where previous versions are stashed for rollback.
func (c *Config) BackupsDir() string {
	return filepath.Join(c.Dir(), "backups")
}

// StagingDir is the scratch area for downloads and unzipping.
func (c *Config) StagingDir() string {
	return filepath.Join(c.Dir(), "staging")
}

// ClientExePath is the full path to the client binary inside current/.
func (c *Config) ClientExePath() string {
	return filepath.Join(c.CurrentDir(), c.ClientExe)
}

// ReadyMarkerPath is the file the client creates on successful startup; its
// absence after launch signals a crash to the launcher.
func (c *Config) ReadyMarkerPath() string {
	return filepath.Join(c.CurrentDir(), c.ReadyMarker)
}

// CrashMarkerPath is the file the launcher creates before spawning the client.
// If it still exists on the next launcher start, the previous run crashed
// before reaching ready-state.
func (c *Config) CrashMarkerPath() string {
	return filepath.Join(c.CurrentDir(), ".crash_marker")
}

// trimBOM removes a leading UTF-8 byte-order mark (EF BB BF) if present.
// Windows tools (Notepad, PowerShell Out-File -Encoding UTF8) emit a BOM
// which json.Unmarshal rejects as an invalid character.
func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
