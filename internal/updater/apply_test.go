package updater

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, p string, entries map[string]string) {
	t.Helper()
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	w := zip.NewWriter(f)
	for n, v := range entries {
		x, e := w.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = x.Write([]byte(v))
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestApplyVersionAndRecovery(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	back := filepath.Join(root, "backups") // legacy location, must not be created
	stage := filepath.Join(root, "stage")
	_ = os.MkdirAll(filepath.Join(cur, "resources"), 0755)
	_ = os.WriteFile(filepath.Join(cur, "Syntee Helper.exe"), []byte("old"), 0644)
	_ = os.WriteFile(filepath.Join(cur, "resources", "app.asar"), []byte("old"), 0644)
	z := filepath.Join(stage, "update.zip")
	_ = os.MkdirAll(stage, 0755)
	writeZip(t, z, map[string]string{"Syntee Helper.exe": "new", "resources/app.asar": "new"})
	if e := ApplyVersion(z, cur, stage, "1", "2", nil); e != nil {
		t.Fatal(e)
	}
	if v := readVersion(cur); v != "2" {
		t.Fatalf("version %q", v)
	}
	if _, err := os.Stat(back); !os.IsNotExist(err) {
		t.Fatalf("retained backup directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "previous")); !os.IsNotExist(err) {
		t.Fatalf("temporary previous tree was not removed: %v", err)
	}
	if v, e := RecoverApply(cur, stage); e != nil || v != "2" {
		t.Fatalf("recover %q %v", v, e)
	}
}
func TestApplyRejectsUnsafeAndIncompleteZip(t *testing.T) {
	for _, name := range []string{"../escape", "C:/evil", "resources/../evil", "resources:stream"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, "stage")
			_ = os.MkdirAll(stage, 0755)
			z := filepath.Join(stage, "x.zip")
			writeZip(t, z, map[string]string{name: "x"})
			e := ApplyVersion(z, filepath.Join(root, "cur"), stage, "", "1", nil)
			if e == nil {
				t.Fatal("accepted unsafe/incomplete archive")
			}
		})
	}
}
func TestRecoveryCompletesTemporarySwap(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	stage := filepath.Join(root, "stage")
	newDir := filepath.Join(stage, "new")
	previous := filepath.Join(stage, "previous")
	_ = os.MkdirAll(filepath.Join(newDir, "resources"), 0755)
	_ = os.MkdirAll(filepath.Join(previous, "resources"), 0755)
	_ = os.WriteFile(filepath.Join(newDir, "Syntee Helper.exe"), []byte("new"), 0644)
	_ = os.WriteFile(filepath.Join(newDir, "resources", "app.asar"), []byte("new"), 0644)
	_ = os.WriteFile(filepath.Join(newDir, ".version"), []byte("2\n"), 0644)
	_ = os.WriteFile(filepath.Join(previous, "Syntee Helper.exe"), []byte("old"), 0644)
	_ = os.WriteFile(filepath.Join(previous, "resources", "app.asar"), []byte("old"), 0644)
	if err := writeJournal(stage, applyJournal{Phase: "previous_moved", Previous: previous, HadCurrent: true, NewVersion: "2"}); err != nil {
		t.Fatal(err)
	}
	if version, err := RecoverApply(cur, stage); err != nil || version != "2" {
		t.Fatalf("recovery version=%q err=%v", version, err)
	}
	data, err := os.ReadFile(filepath.Join(cur, "Syntee Helper.exe"))
	if err != nil || string(data) != "new" {
		t.Fatalf("new current missing or incorrect: %q %v", data, err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("temporary previous tree was not removed: %v", err)
	}
}
