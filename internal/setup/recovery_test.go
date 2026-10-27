package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

func TestRecoveryRemovesLegacyBackupsAndReconcilesVersion(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(config.InstallDir(), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetCurrentVersion("1.0.0")
	current := cfg.CurrentDir()
	if err := os.MkdirAll(filepath.Join(current, "resources"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(current, "Syntee Helper.exe"), []byte("exe"), 0644)
	_ = os.WriteFile(filepath.Join(current, "resources", "app.asar"), []byte("asar"), 0644)
	_ = os.WriteFile(filepath.Join(current, ".version"), []byte("2.0.0\n"), 0644)
	legacy := filepath.Join(cfg.Dir(), "backups", "old")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInstallation(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentVersion != "2.0.0" {
		t.Fatalf("version %s", cfg.CurrentVersion)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir(), "backups")); !os.IsNotExist(err) {
		t.Fatalf("legacy backup directory remains: %v", err)
	}
}

func TestValidateRelease(t *testing.T) {
	if err := ValidateRelease(&config.Config{}, &updater.Manifest{Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRelease(&config.Config{}, nil); err == nil {
		t.Fatal("nil release accepted")
	}
}
