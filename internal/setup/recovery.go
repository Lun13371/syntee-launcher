package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

// RecoverInstallation must run under AcquireInstallLock before checking for a
// missing client or synchronizing updater.exe. Repeated calls are idempotent.
func RecoverInstallation(cfg *config.Config) error {
	version, err := updater.RecoverApply(cfg.CurrentDir(), cfg.StagingDir(), filepath.Join(cfg.Dir(), "backups"))
	if err != nil {
		return fmt.Errorf("recover interrupted update: %w", err)
	}
	// Remove retained version archives from older builds now that legacy
	// interrupted transactions have been recovered.
	if err := os.RemoveAll(filepath.Join(cfg.Dir(), "backups")); err != nil {
		return fmt.Errorf("remove legacy version backups: %w", err)
	}
	if version != "" && version != cfg.CurrentVersion {
		cfg.SetCurrentVersion(version)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save recovered version: %w", err)
		}
	}
	return nil
}

// ValidateRelease ensures the gateway provided a release before download/apply.
func ValidateRelease(_ *config.Config, manifest *updater.Manifest) error {
	if manifest == nil {
		return fmt.Errorf("no release available")
	}
	return nil
}
