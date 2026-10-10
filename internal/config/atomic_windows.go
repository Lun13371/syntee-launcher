//go:build windows

package config

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, e := os.CreateTemp(dir, ".atomic-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(perm); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return retryMove(tmp, path, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func RenameWithRetry(src, dst string) error {
	return retryMove(src, dst, windows.MOVEFILE_WRITE_THROUGH)
}
func retryMove(src, dst string, flags uint32) error {
	var e error
	for i := 0; i < 10; i++ {
		e = windows.MoveFileEx(syscall.StringToUTF16Ptr(src), syscall.StringToUTF16Ptr(dst), flags)
		if e == nil {
			return nil
		}
		if e != windows.ERROR_SHARING_VIOLATION && e != windows.ERROR_ACCESS_DENIED {
			return e
		}
		time.Sleep(time.Duration(i+1) * 30 * time.Millisecond)
	}
	return e
}
