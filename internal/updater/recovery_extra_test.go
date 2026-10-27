package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryRejectsOutsideBackup(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	back := filepath.Join(root, "backups")
	os.MkdirAll(stage, 0755)
	outside := filepath.Join(root, "outside")
	os.MkdirAll(outside, 0755)
	if err := writeJournal(stage, applyJournal{Phase: "backed_up", Backup: outside, HadCurrent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverApply(filepath.Join(root, "current"), stage, back); err == nil {
		t.Fatal("accepted out-of-tree backup")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("outside directory changed")
	}
}
func TestRecoveryKeepsUnrecoverableJournal(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	back := filepath.Join(root, "backups")
	os.MkdirAll(stage, 0755)
	if err := writeJournal(stage, applyJournal{Phase: "backed_up", Backup: filepath.Join(back, "1_1"), HadCurrent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverApply(filepath.Join(root, "current"), stage, back); err == nil {
		t.Fatal("missing backup silently accepted")
	}
	if _, err := os.Stat(filepath.Join(stage, "apply.json")); err != nil {
		t.Fatal("journal lost")
	}
}
func TestApplyRejectsEmptyRequiredFiles(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	os.MkdirAll(stage, 0755)
	z := filepath.Join(stage, "update.zip")
	writeZip(t, z, map[string]string{"Syntee Helper.exe": "", "resources/app.asar": "x"})
	if err := ApplyVersion(z, filepath.Join(root, "current"), stage, "1", "1", nil); err == nil {
		t.Fatal("empty executable accepted")
	}
}
