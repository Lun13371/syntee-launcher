package setup

import (
	"fmt"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

// RecoverInstallation must run under AcquireInstallLock before checking for a
// missing client or synchronizing updater.exe. Repeated calls are idempotent.
func RecoverInstallation(cfg *config.Config) error {
	version, err := updater.RecoverApply(cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir())
	if err != nil {
		return fmt.Errorf("recover interrupted update: %w", err)
	}
	if version != "" && version != cfg.CurrentVersion {
		cfg.SetCurrentVersion(version)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save recovered version: %w", err)
		}
	}
	return nil
}

// ValidateRelease prevents automatic and explicit reinstall paths from
// reapplying a known-crashing version, including immediately after rollback.
func ValidateRelease(cfg *config.Config, manifest *updater.Manifest) error {
	if manifest == nil {
		return fmt.Errorf("no release available")
	}
	if cfg.IsBad(manifest.Version) {
		return fmt.Errorf("release %s is marked bad; refusing installation", manifest.Version)
	}
	return nil
}
