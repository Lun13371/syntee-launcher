package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"github.com/Lun13371/syntee-launcher/internal/config"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxZipEntries = 100000
const maxUnpackedBytes uint64 = 8 << 30

// Apply preserves the legacy API while performing a journaled swap.
func Apply(zipPath, currentDir, stagingDir, oldVersion string, onProgress func(int64, int64)) error {
	return ApplyVersion(zipPath, currentDir, stagingDir, oldVersion, "", onProgress)
}

// ApplyVersion validates the package and swaps it into current/. The previous
// build is held only temporarily in staging/previous during promotion, then deleted.
func ApplyVersion(zipPath, currentDir, stagingDir, oldVersion, newVersion string, onProgress func(int64, int64)) error {
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return err
	}
	if _, err := RecoverApply(currentDir, stagingDir, filepath.Join(filepath.Dir(currentDir), "backups")); err != nil {
		return err
	}
	newDir := filepath.Join(stagingDir, "new")
	if err := os.RemoveAll(newDir); err != nil {
		return fmt.Errorf("clear staging: %w", err)
	}
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return err
	}
	if err := unzip(zipPath, newDir, onProgress); err != nil {
		_ = os.RemoveAll(newDir)
		return fmt.Errorf("unzip: %w", err)
	}
	if err := validateInstall(newDir); err != nil {
		_ = os.RemoveAll(newDir)
		return err
	}
	if newVersion != "" {
		if err := config.AtomicWriteFile(filepath.Join(newDir, ".version"), []byte(newVersion+"\n"), 0644); err != nil {
			return err
		}
	}
	previousDir := filepath.Join(stagingDir, "previous")
	if err := os.RemoveAll(previousDir); err != nil {
		return fmt.Errorf("clear temporary previous tree: %w", err)
	}
	j := applyJournal{Phase: "prepared", Previous: previousDir, NewVersion: newVersion}
	if _, err := os.Stat(currentDir); err == nil {
		j.HadCurrent = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeJournal(stagingDir, j); err != nil {
		return err
	}
	if j.HadCurrent {
		if err := RenameWithRetry(currentDir, previousDir); err != nil {
			return fmt.Errorf("move current to temporary staging: %w", err)
		}
		j.Phase = "previous_moved"
		if err := writeJournal(stagingDir, j); err != nil {
			_ = RenameWithRetry(previousDir, currentDir)
			return err
		}
	}
	if err := RenameWithRetry(newDir, currentDir); err != nil {
		if j.HadCurrent {
			if restoreErr := RenameWithRetry(previousDir, currentDir); restoreErr != nil {
				return fmt.Errorf("promote new current failed: %v; restore temporary previous failed: %w", err, restoreErr)
			}
		}
		return fmt.Errorf("promote staged current: %w", err)
	}
	j.Phase = "installed"
	if err := writeJournal(stagingDir, j); err != nil {
		return err
	}
	if err := os.RemoveAll(previousDir); err != nil {
		return fmt.Errorf("remove temporary previous tree: %w", err)
	}
	_ = os.Remove(zipPath)
	_ = os.Remove(filepath.Join(stagingDir, "apply.json"))
	return nil
}

type applyJournal struct {
	Phase      string `json:"phase"`
	Previous   string `json:"previous,omitempty"`
	Backup     string `json:"backup,omitempty"` // legacy transaction compatibility
	HadCurrent bool   `json:"had_current"`
	NewVersion string `json:"new_version"`
}

func writeJournal(dir string, j applyJournal) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return config.AtomicWriteFile(filepath.Join(dir, "apply.json"), b, 0644)
}

// RecoverApply resolves an interrupted directory swap and reports the installed version when available.
func RecoverApply(currentDir, stagingDir string, legacyBackupsDir ...string) (string, error) {
	p := filepath.Join(stagingDir, "apply.json")
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return readVersion(currentDir), nil
	}
	if e != nil {
		return "", e
	}
	var j applyJournal
	if e = json.Unmarshal(b, &j); e != nil {
		return "", fmt.Errorf("invalid apply journal: %w", e)
	}
	if j.Phase != "prepared" && j.Phase != "previous_moved" && j.Phase != "previous_removed" && j.Phase != "backed_up" && j.Phase != "installed" {
		return "", fmt.Errorf("unknown apply journal phase %q", j.Phase)
	}
	previous := filepath.Join(stagingDir, "previous")
	if j.Previous != "" {
		want, err := filepath.Abs(previous)
		if err != nil {
			return "", err
		}
		got, err := filepath.Abs(j.Previous)
		if err != nil || !strings.EqualFold(got, want) {
			return "", fmt.Errorf("temporary previous path outside staging directory")
		}
	}
	// Legacy journal entries may point into the old backups directory. Accept
	// them only for recovery of that in-flight transaction, never for new saves.
	legacyBackup := ""
	if j.Backup != "" {
		if len(legacyBackupsDir) == 0 {
			return "", fmt.Errorf("legacy backup journal requires legacy backup root")
		}
		root, err := filepath.Abs(legacyBackupsDir[0])
		if err != nil {
			return "", err
		}
		legacyBackup, e = filepath.Abs(j.Backup)
		if e != nil || !strings.EqualFold(filepath.Dir(legacyBackup), root) {
			return "", fmt.Errorf("legacy backup path outside legacy backups directory")
		}
	}
	for _, dir := range []string{currentDir, previous, legacyBackup} {
		if dir == "" {
			continue
		}
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("recovery directory is a link: %s", dir)
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	newDir := filepath.Join(stagingDir, "new")
	currentExists := pathExists(currentDir)
	if j.Phase == "prepared" && !currentExists && j.HadCurrent {
		// The current directory was moved before the journal phase update.
		if pathExists(previous) {
			if err := RenameWithRetry(previous, currentDir); err != nil {
				return "", err
			}
			currentExists = true
		} else if j.Backup != "" && pathExists(legacyBackup) {
			if err := RenameWithRetry(legacyBackup, currentDir); err != nil {
				return "", err
			}
			currentExists = true
		}
	}
	if (j.Phase == "previous_moved" || j.Phase == "backed_up") && !currentExists {
		if err := validateInstall(newDir); err == nil {
			if err := RenameWithRetry(newDir, currentDir); err != nil {
				return "", fmt.Errorf("recover staged current: %w", err)
			}
			currentExists = true
		} else if pathExists(previous) {
			if err := RenameWithRetry(previous, currentDir); err != nil {
				return "", err
			}
			currentExists = true
		} else if pathExists(legacyBackup) {
			if err := RenameWithRetry(legacyBackup, currentDir); err != nil {
				return "", err
			}
			currentExists = true
		} else {
			return "", fmt.Errorf("interrupted update has no current or recoverable staged package")
		}
	}
	if !currentExists && (j.Phase == "previous_removed" || j.Phase == "backed_up" || j.Phase == "installed") {
		if err := validateInstall(newDir); err != nil {
			return "", fmt.Errorf("interrupted update lost current and staged package: %w", err)
		}
		if err := RenameWithRetry(newDir, currentDir); err != nil {
			return "", err
		}
		currentExists = true
	}
	if currentExists {
		if err := validateInstall(currentDir); err != nil {
			return "", err
		}
	}
	if e = os.RemoveAll(newDir); e != nil {
		return "", e
	}
	if j.Phase == "installed" || currentExists {
		if e = os.RemoveAll(previous); e != nil {
			return "", e
		}
	}
	if j.Backup != "" && legacyBackup != "" {
		_ = os.RemoveAll(legacyBackup)
	}
	if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
		return "", e
	}
	return readVersion(currentDir), nil
}
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readVersion(dir string) string {
	b, e := os.ReadFile(filepath.Join(dir, ".version"))
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func validateInstall(dir string) error {
	for _, name := range []string{"Syntee Helper.exe", filepath.Join("resources", "app.asar")} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("missing package file %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("package file %s is empty or not regular", name)
		}
	}
	return nil
}

// The new package is staged and validated before the old current directory is removed.

func unzip(zipPath, dst string, onProgress func(int64, int64)) error {
	r, e := zip.OpenReader(zipPath)
	if e != nil {
		return e
	}
	defer r.Close()
	if len(r.File) > maxZipEntries {
		return fmt.Errorf("zip has too many entries")
	}
	var total uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > maxUnpackedBytes-total {
			return fmt.Errorf("zip unpacked size limit exceeded")
		}
		total += f.UncompressedSize64
		if _, e = safeJoin(dst, f.Name); e != nil {
			return fmt.Errorf("unsafe path %q: %w", f.Name, e)
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeType != 0 && !mode.IsDir() {
			return fmt.Errorf("unsupported zip entry type %q", f.Name)
		}
	}
	free, err := availableDiskBytes(dst)
	if err != nil {
		return fmt.Errorf("check disk space: %w", err)
	}
	if free < total+(16<<20) {
		return fmt.Errorf("insufficient disk space: need %d bytes, have %d", total+(16<<20), free)
	}
	var done int64
	for _, f := range r.File {
		p, _ := safeJoin(dst, f.Name)
		if f.FileInfo().IsDir() {
			if e = os.MkdirAll(p, 0755); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			return e
		}
		if e = extractFile(f, p); e != nil {
			return e
		}
		done += int64(f.UncompressedSize64)
		if onProgress != nil {
			onProgress(done, int64(total))
		}
	}
	return nil
}
func extractFile(f *zip.File, dest string) error {
	rc, e := f.Open()
	if e != nil {
		return e
	}
	defer rc.Close()
	out, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if e != nil {
		return e
	}
	var written int64
	written, e = io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if e == nil && written != int64(f.UncompressedSize64) {
		e = fmt.Errorf("zip entry size mismatch")
	}
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e == nil {
		e = ce
	}
	return e
}
func safeJoin(dst, name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") || strings.HasPrefix(n, "//") {
		return "", fmt.Errorf("absolute path or alternate data stream")
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." || part == "." {
			return "", fmt.Errorf("traversal path")
		}
		if part != "" && (strings.TrimRight(part, " .") != part || strings.ContainsAny(part, "<>\"|?*\x00")) {
			return "", fmt.Errorf("invalid Windows filename")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return "", fmt.Errorf("reserved Windows filename")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(n))
	if clean == "." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid path")
	}
	joined := filepath.Join(dst, clean)
	rel, e := filepath.Rel(dst, joined)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes destination")
	}
	return joined, nil
}

const renameBackoffTries = 12

// RenameWithRetry bounds transient Windows sharing violations during a swap.
func RenameWithRetry(src, dst string) error { return renameWithRetry(src, dst, renameBackoffTries) }
func renameWithRetry(src, dst string, tries int) error {
	wait := 200 * time.Millisecond
	var last error
	for i := 0; i < tries; i++ {
		last = os.Rename(src, dst)
		if last == nil {
			return nil
		}
		if !isAccessError(last) {
			return last
		}
		if i+1 < tries {
			time.Sleep(wait)
		}
		wait *= 2
		if wait > 2*time.Second {
			wait = 2 * time.Second
		}
	}
	return last
}
func isAccessError(e error) bool {
	if e == nil {
		return false
	}
	if le, ok := e.(*os.LinkError); ok {
		if n, ok := le.Err.(syscall.Errno); ok && (uint32(n) == 5 || uint32(n) == 32) {
			return true
		}
	}
	s := e.Error()
	return strings.Contains(s, "being used by another process") || strings.Contains(s, "Access is denied")
}
