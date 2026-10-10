//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"time"
)

func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(perm); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	for i := 0; i < 8; i++ {
		err = os.Rename(tmp, path)
		if err == nil {
			return syncDir(dir)
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}
func RenameWithRetry(src, dst string) error {
	var err error
	for i := 0; i < 8; i++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}
func syncDir(dir string) error {
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func filepathDir(p string) string {
	d := os.TempDir()
	if i := lastSep(p); i >= 0 {
		d = p[:i]
	}
	return d
}
func lastSep(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return i
		}
	}
	return -1
}
