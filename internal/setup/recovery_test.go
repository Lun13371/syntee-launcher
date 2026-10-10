package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

func TestRecoveryRestoresMissingCurrentAndVersion(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(config.InstallDir(), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetCurrentVersion("2.0.0")
	backup := filepath.Join(cfg.BackupsDir(), "1.0.0_123")
	for _, path := range []string{filepath.Join(backup, "resources"), cfg.StagingDir()} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"Syntee Helper.exe": "exe", "resources/app.asar": "asar", ".version": "1.0.0\n"} {
		if err := os.WriteFile(filepath.Join(backup, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	journal, _ := json.Marshal(map[string]any{"phase": "backed_up", "backup": backup, "had_current": true, "new_version": "2.0.0"})
	if err := os.WriteFile(filepath.Join(cfg.StagingDir(), "apply.json"), journal, 0644); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInstallation(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentVersion != "1.0.0" {
		t.Fatalf("version %s", cfg.CurrentVersion)
	}
	if !clientInstalled(cfg) {
		t.Fatal("current not restored")
	}
	if err := RecoverInstallation(cfg); err != nil {
		t.Fatal("non-idempotent recovery:", err)
	}
	loaded, err := config.Load()
	if err != nil || loaded.CurrentVersion != "1.0.0" {
		t.Fatalf("version not persisted: %+v %v", loaded, err)
	}
}

func TestKnownBadReleaseRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.AddBadVersion("2.0.0")
	if err := ValidateRelease(cfg, &updater.Manifest{Version: "2.0.0", Force: true}); err == nil {
		t.Fatal("bad forced release accepted")
	}
	if err := ValidateRelease(cfg, &updater.Manifest{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRelease(cfg, nil); err == nil {
		t.Fatal("nil release accepted")
	}
}
