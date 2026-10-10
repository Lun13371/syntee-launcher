package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"golang.org/x/sys/windows"
)

func TestAtomicCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	os.WriteFile(src, []byte("new"), 0644)
	os.WriteFile(dst, []byte("old"), 0644)
	// An open destination without delete sharing forces replacement failure.
	name, _ := windows.UTF16PtrFromString(dst)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = CopyFileAtomic(src, dst)
	windows.CloseHandle(h)
	if err == nil {
		t.Fatal("expected locked replacement to fail")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "old" {
		t.Fatal("old destination lost")
	}
	if err := CopyFileAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dst)
	if err := CopyFileAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	same, _ := os.Stat(dst)
	if !same.ModTime().Equal(info.ModTime()) {
		t.Fatal("identical copy changed timestamp")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("temporary file leaked")
	}
}

func TestInstallOptionsRoundTrip(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(config.InstallDir(), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(cfg.Dir(), 0755)
	opts := InstallOptions{DesktopShortcut: false, StartMenuShortcut: true, Autostart: false}
	if err := SaveInstallOptions(cfg, opts); err != nil {
		t.Fatal(err)
	}
	actual, err := LoadInstallOptions(cfg)
	if err != nil || actual != opts {
		t.Fatalf("%+v %v", actual, err)
	}
}

func TestLogRotationBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	l, err := OpenRotatingLog(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := l.Write([]byte("0123456789abcdef")); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	for _, suffix := range []string{"", ".1", ".2"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 32 {
			t.Fatal("unbounded log")
		}
	}
}
