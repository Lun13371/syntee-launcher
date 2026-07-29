package updater

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Apply replaces the current/ directory with the contents of the downloaded
// zip. The previous current/ is moved to backups/<old_version>/ so rollback
// is instant and the old build survives even if the new one is deleted.
//
// Sequence (all on the same filesystem so renames are atomic):
//  1. unzip staging/update.zip -> staging/new/
//  2. move current/ -> backups/<old_version>_<timestamp>/
//  3. move staging/new/ -> current/
//  4. clean staging/
//
// If step 2 fails (e.g. current/ missing on first install) we proceed anyway.
// If step 3 fails we attempt to restore from the backup we just made.
//
// onProgress, if non-nil, is called with (bytesExtracted, totalUncompressedBytes)
// during the unzip phase so the GUI can show extraction progress.
func Apply(zipPath, currentDir, backupsDir, stagingDir, oldVersion string, onProgress func(extracted, total int64)) error {
	newDir := filepath.Join(stagingDir, "new")
	// Clean any leftover from a previous attempt.
	os.RemoveAll(newDir)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return fmt.Errorf("create new dir: %w", err)
	}

	if err := unzip(zipPath, newDir, onProgress); err != nil {
		return fmt.Errorf("unzip: %w", err)
	}

	// Backup current/ if it exists. On first install there's nothing to back up.
	backupName := fmt.Sprintf("%s_%d", sanitizeVersion(oldVersion), time.Now().Unix())
	backupDir := filepath.Join(backupsDir, backupName)
	if _, err := os.Stat(currentDir); err == nil {
		if err := os.MkdirAll(backupsDir, 0o755); err != nil {
			return fmt.Errorf("create backups dir: %w", err)
		}
		if err := os.Rename(currentDir, backupDir); err != nil {
			return fmt.Errorf("backup current -> %s: %w", backupDir, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat current dir: %w", err)
	}

	// Atomic swap: staging/new -> current.
	if err := os.Rename(newDir, currentDir); err != nil {
		// Try to restore the backup so we don't leave the install without a client.
		if restoreErr := os.Rename(backupDir, currentDir); restoreErr != nil {
			return fmt.Errorf("swap failed (%v) AND restore failed (%v) — install is in a bad state", err, restoreErr)
		}
		return fmt.Errorf("swap new -> current (restored backup): %w", err)
	}

	// Clean the staging zip; keep staging/new already gone via the rename.
	_ = os.Remove(zipPath)

	// Prune old backups — keep only the 2 most recent.
	// Each backup is ~400 MB, so without pruning this dir grows unbounded.
	pruneBackups(backupsDir, 2)

	return nil
}

// pruneBackups removes all but the keepCount most recent backup directories.
// Backups are named "<version>_<unix_ts>". Directories starting with "bad_"
// are also cleaned up (they're crash-loop forensics, not useful long-term).
func pruneBackups(backupsDir string, keepCount int) {
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return
	}
	type backup struct {
		name string
		path string
		ts   int64
	}
	var backups []backup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Remove "bad_" forensics dirs older than 1 day.
		if strings.HasPrefix(name, "bad_") {
			info, err := e.Info()
			if err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(backupsDir, name))
			}
			continue
		}
		idx := strings.LastIndex(name, "_")
		if idx <= 0 {
			continue
		}
		var ts int64
		fmt.Sscanf(name[idx+1:], "%d", &ts)
		backups = append(backups, backup{name: name, path: filepath.Join(backupsDir, name), ts: ts})
	}
	if len(backups) <= keepCount {
		return
	}
	// Sort by timestamp descending (newest first).
	sort.Slice(backups, func(i, j int) bool { return backups[i].ts > backups[j].ts })
	// Remove everything after the first keepCount.
	for i := keepCount; i < len(backups); i++ {
		log.Printf("prune: removing old backup %s", backups[i].name)
		_ = os.RemoveAll(backups[i].path)
	}
}

// unzip extracts every entry of a zip archive into dst. It rejects absolute
// paths and ".." traversal to prevent writing outside dst (zip-slip). File
// permissions are preserved on Unix; on Windows the mode bits are ignored.
//
// onProgress, if non-nil, receives (bytesExtracted, totalUncompressed) where
// total is the sum of all file UncompressedSize64 values. The first pass
// computes the total; the second pass extracts and reports progress.
func unzip(zipPath, dst string, onProgress func(extracted, total int64)) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	// First pass: compute total uncompressed size for progress reporting.
	var total int64
	if onProgress != nil {
		for _, f := range r.File {
			if !f.FileInfo().IsDir() {
				total += int64(f.UncompressedSize64)
			}
		}
	}

	var extracted int64
	for _, f := range r.File {
		name, err := safeJoin(dst, f.Name)
		if err != nil {
			return fmt.Errorf("unsafe path %q: %w", f.Name, err)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, name); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
		if onProgress != nil && !f.FileInfo().IsDir() {
			extracted += int64(f.UncompressedSize64)
			onProgress(extracted, total)
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// safeJoin prevents zip-slip: the cleaned entry path must stay within dst.
func safeJoin(dst, name string) (string, error) {
	clean := filepath.Clean(name)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("path escapes dst")
	}
	joined := filepath.Join(dst, clean)
	// Final belt-and-braces check.
	rel, err := filepath.Rel(dst, joined)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path escapes dst")
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
