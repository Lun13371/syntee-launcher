// Package rollback restores a previous client build when the current one
// crashes too many times in a row. The launcher detects crashes via a marker
// file: before spawning the client it creates current/.crash_marker; the
// client deletes it once it reaches ready-state. If the marker is still there
// on the next launcher start, the previous run crashed.
package rollback

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

// RestoreIfBad swaps current/ back to the most recent backup when the current
// version is in cfg.BadVersions. It returns the restored version (or "" if
// no restore happened). After a successful restore the current_version is
// updated and the bad entry is left in place so the launcher won't retry it.
func RestoreIfBad(cfg *config.Config) (string, error) {
	if !cfg.IsBad(cfg.CurrentVersion) {
		return "", nil
	}
	backup, version, err := latestBackup(cfg)
	if err != nil {
		return "", err
	}
	if backup == "" {
		return "", fmt.Errorf("version %s is marked bad but no backup exists — manual reinstall needed", cfg.CurrentVersion)
	}
	// Move the bad current aside (don't lose it for forensics).
	badDir := filepath.Join(cfg.BackupsDir(), fmt.Sprintf("bad_%s_%d", sanitize(cfg.CurrentVersion), time.Now().Unix()))
	if err := updater.RenameWithRetry(cfg.CurrentDir(), badDir); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("move bad current aside: %w", err)
	}
	if err := updater.RenameWithRetry(backup, cfg.CurrentDir()); err != nil {
		// Try to put the bad one back so we don't end up with nothing.
		_ = updater.RenameWithRetry(badDir, cfg.CurrentDir())
		return "", fmt.Errorf("restore backup -> current: %w", err)
	}
	cfg.SetCurrentVersion(version)
	return version, nil
}

// latestBackup returns the path and version tag of the newest backup folder.
// Backups are named <version>_<unix_ts> — we sort by the timestamp suffix.
func latestBackup(cfg *config.Config) (path string, version string, err error) {
	entries, err := os.ReadDir(cfg.BackupsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	type cand struct {
		path    string
		version string
		ts      int64
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "bad_") || !validBackup(filepath.Join(cfg.BackupsDir(), name)) {
			continue
		}
		// name = "<version>_<unix_ts>"
		idx := strings.LastIndex(name, "_")
		if idx <= 0 {
			continue
		}
		var ts int64
		if _, err := fmt.Sscanf(name[idx+1:], "%d", &ts); err != nil {
			continue
		}
		if ts < 1000000000000 {
			ts *= int64(time.Second)
		}
		version := name[:idx]
		if raw, err := os.ReadFile(filepath.Join(cfg.BackupsDir(), name, ".version")); err == nil && strings.TrimSpace(string(raw)) != "" {
			version = strings.TrimSpace(string(raw))
		}
		if cfg.IsBad(version) {
			continue
		}
		cands = append(cands, cand{
			path:    filepath.Join(cfg.BackupsDir(), name),
			version: version,
			ts:      ts,
		})
	}
	if len(cands) == 0 {
		return "", "", nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].ts > cands[j].ts })
	return cands[0].path, cands[0].version, nil
}

func validBackup(path string) bool {
	exe, err := os.Stat(filepath.Join(path, "Syntee Helper.exe"))
	if err != nil || !exe.Mode().IsRegular() || exe.Size() == 0 {
		return false
	}
	res, err := os.Stat(filepath.Join(path, "resources", "app.asar"))
	return err == nil && res.Mode().IsRegular() && res.Size() > 0
}

func sanitize(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, v)
}
