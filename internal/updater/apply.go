package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"github.com/Lun13371/syntee-launcher/internal/config"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const maxZipEntries = 100000
const maxUnpackedBytes uint64 = 8 << 30

// Apply preserves the legacy API while performing a journaled swap.
func Apply(zipPath, currentDir, backupsDir, stagingDir, oldVersion string, onProgress func(int64, int64)) error {
	return ApplyVersion(zipPath, currentDir, backupsDir, stagingDir, oldVersion, "", onProgress)
}

// ApplyVersion extracts and validates the package, journals every swap phase and
// records the installed version in current/.version.
func ApplyVersion(zipPath, currentDir, backupsDir, stagingDir, oldVersion, newVersion string, onProgress func(int64, int64)) error {
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(backupsDir, 0755); err != nil {
		return err
	}
	if _, err := RecoverApply(currentDir, backupsDir, stagingDir); err != nil {
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
	backupDir := filepath.Join(backupsDir, fmt.Sprintf("%s_%d", sanitizeVersion(oldVersion), time.Now().UnixNano()))
	j := applyJournal{Phase: "prepared", Backup: backupDir, HadCurrent: false, NewVersion: newVersion}
	if _, e := os.Stat(currentDir); e == nil {
		j.HadCurrent = true
	} else if !os.IsNotExist(e) {
		return e
	}
	if err := writeJournal(stagingDir, j); err != nil {
		return err
	}
	if j.HadCurrent {
		if oldVersion != "" {
			if err := config.AtomicWriteFile(filepath.Join(currentDir, ".version"), []byte(oldVersion+"\n"), 0644); err != nil {
				return fmt.Errorf("record backup version: %w", err)
			}
		}
		if err := RenameWithRetry(currentDir, backupDir); err != nil {
			return err
		}
		j.Phase = "backed_up"
		if err := writeJournal(stagingDir, j); err != nil {
			_ = RenameWithRetry(backupDir, currentDir)
			return err
		}
	}
	if err := RenameWithRetry(newDir, currentDir); err != nil {
		if j.HadCurrent {
			if r := RenameWithRetry(backupDir, currentDir); r != nil {
				return fmt.Errorf("swap failed: %v; restore failed: %w", err, r)
			}
		}
		return fmt.Errorf("swap new -> current: %w", err)
	}
	j.Phase = "installed"
	if err := writeJournal(stagingDir, j); err != nil {
		return err
	}
	_ = os.Remove(zipPath)
	_ = os.Remove(filepath.Join(stagingDir, "apply.json"))
	pruneBackups(backupsDir, 2)
	return nil
}

type applyJournal struct {
	Phase      string `json:"phase"`
	Backup     string `json:"backup"`
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
func RecoverApply(currentDir, backupsDir, stagingDir string) (string, error) {
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
	if j.Phase != "prepared" && j.Phase != "backed_up" && j.Phase != "installed" {
		return "", fmt.Errorf("unknown apply journal phase %q", j.Phase)
	}
	root, err := filepath.Abs(backupsDir)
	if err != nil {
		return "", err
	}
	backup, err := filepath.Abs(j.Backup)
	if err != nil || !strings.EqualFold(filepath.Dir(backup), root) || j.Backup == "" {
		return "", fmt.Errorf("backup path outside backups directory")
	}
	for _, dir := range []string{currentDir, backup} {
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("recovery directory is a link: %s", dir)
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	_, currentErr := os.Stat(currentDir)
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return "", currentErr
	}
	if os.IsNotExist(currentErr) && j.HadCurrent {
		if err := validateInstall(backup); err != nil {
			return "", fmt.Errorf("cannot recover missing current: %w", err)
		}
		if e = RenameWithRetry(backup, currentDir); e != nil {
			return "", e
		}
	} else if os.IsNotExist(currentErr) && j.Phase == "installed" {
		return "", fmt.Errorf("installed transaction has no current directory")
	}
	if j.Phase != "prepared" || !j.HadCurrent && currentErr == nil {
		if e = validateInstall(currentDir); e != nil {
			return "", e
		}
	}
	if e = os.RemoveAll(filepath.Join(stagingDir, "new")); e != nil {
		return "", e
	}
	if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
		return "", e
	}
	return readVersion(currentDir), nil
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

func pruneBackups(backupsDir string, keepCount int) {
	entries, e := os.ReadDir(backupsDir)
	if e != nil {
		return
	}
	type item struct {
		p string
		t int64
	}
	var a []item
	for _, x := range entries {
		if !x.IsDir() {
			continue
		}
		if strings.HasPrefix(x.Name(), "bad_") {
			if info, err := x.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(backupsDir, x.Name()))
			}
			continue
		}
		idx := strings.LastIndex(x.Name(), "_")
		if idx < 1 {
			continue
		}
		var t int64
		if _, e := fmt.Sscanf(x.Name()[idx+1:], "%d", &t); e != nil {
			continue
		}
		if t < 1000000000000 {
			t *= int64(time.Second)
		}
		if !validBackup(filepath.Join(backupsDir, x.Name())) {
			continue
		}
		a = append(a, item{filepath.Join(backupsDir, x.Name()), t})
	}
	sort.Slice(a, func(i, j int) bool { return a[i].t > a[j].t })
	if keepCount < 0 {
		keepCount = 0
	}
	if len(a) <= keepCount {
		return
	}
	for _, x := range a[keepCount:] {
		_ = os.RemoveAll(x.p)
	}
}
func validBackup(p string) bool { return validateInstall(p) == nil }

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
func sanitizeVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, v)
}

const renameBackoffTries = 12

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
