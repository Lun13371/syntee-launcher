package setup

import (
	"bytes"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// CopyFileAtomic preserves dst if copying or replacement fails; identical files
// are left untouched (important when Windows has the executable open).
func CopyFileAtomic(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(data, old) {
		return nil
	}
	return writeAtomic(dst, data, 0755)
}

func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(dst), ".syntee-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
