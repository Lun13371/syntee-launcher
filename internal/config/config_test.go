package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingConfigReturnsDefaults(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	c, err := Load()
	if err != nil || c == nil || c.Channel != "stable" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

// The client spawns <install>/current/updater.exe for its restart-to-update
// flow. Treating that copy as "not installed" made it self-install over the
// launcher (failing on the locked file while the launcher was running) instead
// of updating, so both installation locations must count.
func TestIsInstalledExe(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	install := InstallDir()

	cases := []struct {
		exe  string
		want bool
	}{
		{filepath.Join(install, "syntee-launcher.exe"), true},
		{filepath.Join(install, "SYNTEE-LAUNCHER.EXE"), true},
		{filepath.Join(install, "current", "updater.exe"), true},
		{filepath.Join(install, "current", "UPDATER.EXE"), true},
		// A fresh download that has not been installed yet.
		{filepath.Join(base, "Downloads", "syntee-launcher.exe"), false},
		// Not the updater copy: something else inside current/.
		{filepath.Join(install, "current", "Syntee Helper.exe"), false},
		// updater.exe outside the install tree.
		{filepath.Join(base, "Downloads", "updater.exe"), false},
	}
	for _, c := range cases {
		if got := IsInstalledExe(c.exe); got != c.want {
			t.Errorf("IsInstalledExe(%q) = %v, want %v", c.exe, got, c.want)
		}
	}
}

func TestAtomicWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := AtomicWriteFile(p, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(p, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	b, e := os.ReadFile(p)
	if e != nil || string(b) != "second" {
		t.Fatalf("read %q, %v", b, e)
	}
}
func TestLoadRecoversBackupAndSaveDoesNotReplaceBackupWithCorruptPrimary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	p := filepath.Join(InstallDir(), "launcher.json")
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	good := []byte(`{"channel":"beta","custom_setting":42}`)
	if e := os.WriteFile(p+".bak", good, 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte("{"), 0644); e != nil {
		t.Fatal(e)
	}
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.Channel != "beta" {
		t.Fatalf("channel %q", c.Channel)
	}
	if e = c.Save(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p + ".bak")
	if e != nil || string(b) != string(good) {
		t.Fatalf("backup overwritten: %q %v", b, e)
	}
}
