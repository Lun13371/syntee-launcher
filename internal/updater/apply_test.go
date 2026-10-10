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
	back := filepath.Join(root, "backups")
	stage := filepath.Join(root, "stage")
	_ = os.MkdirAll(filepath.Join(cur, "resources"), 0755)
	_ = os.WriteFile(filepath.Join(cur, "Syntee Helper.exe"), []byte("old"), 0644)
	_ = os.WriteFile(filepath.Join(cur, "resources", "app.asar"), []byte("old"), 0644)
	z := filepath.Join(stage, "update.zip")
	_ = os.MkdirAll(stage, 0755)
	writeZip(t, z, map[string]string{"Syntee Helper.exe": "new", "resources/app.asar": "new"})
	if e := ApplyVersion(z, cur, back, stage, "1", "2", nil); e != nil {
		t.Fatal(e)
	}
	if v := readVersion(cur); v != "2" {
		t.Fatalf("version %q", v)
	}
	if v, e := RecoverApply(cur, back, stage); e != nil || v != "2" {
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
			e := ApplyVersion(z, filepath.Join(root, "cur"), filepath.Join(root, "back"), stage, "", "1", nil)
			if e == nil {
				t.Fatal("accepted unsafe/incomplete archive")
			}
		})
	}
}
func TestRecoveryRestoresBackup(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	back := filepath.Join(root, "backups", "v1_1")
	stage := filepath.Join(root, "stage")
	_ = os.MkdirAll(filepath.Join(back, "resources"), 0755)
	_ = os.MkdirAll(stage, 0755)
	_ = os.WriteFile(filepath.Join(back, "Syntee Helper.exe"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(back, "resources", "app.asar"), []byte("x"), 0644)
	if e := writeJournal(stage, applyJournal{Phase: "backed_up", Backup: back, HadCurrent: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := RecoverApply(cur, filepath.Dir(back), stage); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(cur, "Syntee Helper.exe")); e != nil {
		t.Fatal(e)
	}
}
