package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"golang.org/x/sys/windows/registry"
)

// Preferences are intentionally separate from updater version state.
func SaveInstallOptions(cfg *config.Config, opts InstallOptions) error {
	data, err := json.MarshalIndent(opts, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(cfg.Dir(), "install-options.json"), data, 0644)
}

func LoadInstallOptions(cfg *config.Config) (InstallOptions, error) {
	var opts InstallOptions
	path := filepath.Join(cfg.Dir(), "install-options.json")
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &opts); err != nil {
			return opts, fmt.Errorf("read install options: %w", err)
		}
		return opts, nil
	}
	if !os.IsNotExist(err) {
		return opts, err
	}
	// Legacy migration infers the current choices; never enables missing items.
	opts.DesktopShortcut = preferenceFileExists(filepath.Join(os.Getenv("USERPROFILE"), "Desktop", "Syntee Helper.lnk"))
	opts.StartMenuShortcut = preferenceFileExists(filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper.lnk"))
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRegPath, registry.QUERY_VALUE)
	if err == nil {
		value, _, e := k.GetStringValue("SynteeHelper")
		opts.Autostart = e == nil && value != ""
		k.Close()
	}
	if err := SaveInstallOptions(cfg, opts); err != nil {
		return opts, err
	}
	return opts, nil
}

func preferenceFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
