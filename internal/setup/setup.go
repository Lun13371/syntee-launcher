// Package setup handles first-run installation: downloading the client zip
// from the gateway, extracting it into current/, and registering the app in
// Windows (shortcuts, autostart, "Programs and Features" uninstall entry).
//
// The launcher IS the installer — there is no separate NSIS/Electron-builder
// installer. The user downloads syntee-launcher.exe (7 MB) from the website,
// runs it, and it fetches the full client (~400 MB zip) on first launch.
// Subsequent launches skip the download and go straight to running the client.
package setup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/updater"
	"golang.org/x/sys/windows/registry"
)

// InstallOptions controls which Windows integration features are created.
type InstallOptions struct {
	DesktopShortcut   bool
	StartMenuShortcut bool
	Autostart         bool
}

// RunIfNeeded performs first-run setup if the client is not yet installed.
// Uses default options (all features enabled). Kept for backward compat.
func RunIfNeeded(ctx context.Context, cfg *config.Config, client *updater.Client,
	onProgress func(done, total int64), onPhase func(phase string)) error {
	opts := InstallOptions{DesktopShortcut: true, StartMenuShortcut: true, Autostart: true}
	if clientInstalled(cfg) || preferenceFileExists(filepath.Join(cfg.Dir(), "install-options.json")) {
		var err error
		opts, err = LoadInstallOptions(cfg)
		if err != nil {
			return err
		}
	}
	return RunIfNeededWithOptions(ctx, cfg, client, opts, onProgress, onPhase)
}

// RunIfNeededWithOptions performs first-run setup with user-selected options.
func RunIfNeededWithOptions(ctx context.Context, cfg *config.Config, client *updater.Client,
	opts InstallOptions, onProgress func(done, total int64), onPhase func(phase string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := RecoverInstallation(cfg); err != nil {
		return err
	}
	if err := SaveInstallOptions(cfg, opts); err != nil {
		return fmt.Errorf("save install options: %w", err)
	}
	needsClient := !clientInstalled(cfg)
	// If we're downloading a fresh client, always (re)create shortcuts
	// — even if old registry keys exist from a previous install.
	needsSetup := !setupDone(cfg) || needsClient

	if !needsClient && !needsSetup {
		return nil
	}

	// ── Step 1: download + extract client if missing ─────────────────────
	if needsClient {
		log.Printf("first run: client not found, downloading from gateway")
		if onPhase != nil {
			onPhase("download")
		}

		manifest, err := client.Check(ctx, cfg.Channel, "0.0.0")
		if err != nil {
			return fmt.Errorf("fetch release manifest: %w", err)
		}
		if manifest == nil {
			return fmt.Errorf("no active release on channel %q — cannot install", cfg.Channel)
		}

		if err := ValidateRelease(cfg, manifest); err != nil {
			return err
		}
		res, err := client.DownloadToStaging(ctx, manifest, cfg.StagingDir(), onProgress)
		if err != nil {
			return fmt.Errorf("download client: %w", err)
		}
		log.Printf("downloaded client %s (%s) in %s", manifest.Version, res.SHA256, res.Duration)

		if onPhase != nil {
			onPhase("extract")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := updater.ApplyVersion(res.Path, cfg.CurrentDir(), cfg.StagingDir(), cfg.CurrentVersion, manifest.Version, onProgress); err != nil {
			return fmt.Errorf("extract client: %w", err)
		}

		cfg.SetCurrentVersion(manifest.Version)
		if err := cfg.Save(); err != nil {
			log.Printf("save config after install: %v", err)
		}
		copyUpdater(cfg)
		log.Printf("client installed: version %s", manifest.Version)
	}

	// ── Step 2: Windows integration (shortcuts, autostart, uninstall entry)
	if needsSetup {
		log.Printf("first run: registering app in Windows")
		if onPhase != nil {
			onPhase("register")
		}
		// Remove old shortcuts that might point to the launcher.
		removeOldShortcuts()
		if err := registerApp(cfg, opts); err != nil {
			log.Printf("setup warning: %v", err)
		}
		markSetupDone(cfg)
	}

	return nil
}

// copyUpdater copies the launcher binary into current/updater.exe so the
// client can always find an updater next to itself. Called after install.
func copyUpdater(cfg *config.Config) {
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("copyUpdater: cannot determine exe path: %v", err)
		return
	}
	dst := filepath.Join(cfg.CurrentDir(), "updater.exe")
	if err := copyFile(exePath, dst); err != nil {
		log.Printf("copyUpdater: %v", err)
		return
	}
	log.Printf("copyUpdater: copied -> %s", dst)
}

func copyFile(src, dst string) error { return CopyFileAtomic(src, dst) }

// clientInstalled returns true if current/<client_exe> exists and is non-empty.
func clientInstalled(cfg *config.Config) bool {
	info, err := os.Stat(cfg.ClientExePath())
	return err == nil && !info.IsDir() && info.Size() > 0
}

// setupDone checks whether the Windows integration has been performed by
// looking for the uninstall registry key. If the key exists we assume
// shortcuts and autostart were created too.
func setupDone(cfg *config.Config) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallRegPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// markSetupDone writes a marker file so we don't re-check the registry on
// every launch (the registry check is fast, but the marker makes the intent
// explicit and survives registry cleans).
func markSetupDone(cfg *config.Config) {
	marker := filepath.Join(cfg.Dir(), ".setup_done")
	_ = os.WriteFile(marker, []byte("ok"), 0o644)
}

// EnsureShortcuts always recreates desktop and start menu shortcuts
// pointing at the client exe. Called on every launcher start when the
// client is already installed. This guarantees shortcuts are correct
// even after upgrading from an older launcher that pointed them at
// the launcher binary.
func EnsureShortcuts(cfg *config.Config) error {
	opts, err := LoadInstallOptions(cfg)
	if err != nil {
		return err
	}
	log.Printf("ensuring selected shortcuts point to client exe")
	removeOldShortcuts()
	return registerApp(cfg, opts)
}

// removeOldShortcuts deletes existing shortcuts so they can be recreated
// pointing at the client exe (not the launcher). This handles the case
// where a previous install created shortcuts pointing to the launcher.
func removeOldShortcuts() {
	startMenu := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper.lnk")
	desktop := filepath.Join(os.Getenv("USERPROFILE"), "Desktop", "Syntee Helper.lnk")
	_ = os.Remove(startMenu)
	_ = os.Remove(desktop)
	// Remove old autostart entry (might point to launcher).
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRegPath, registry.SET_VALUE)
	if err == nil {
		_ = k.DeleteValue("SynteeHelper")
		k.Close()
	}
}

// RecreateShortcuts deletes and recreates all shortcuts and autostart,
// pointing them at the client exe (not the launcher). Called after reinstall
// or when shortcuts are stale.
func RecreateShortcuts(cfg *config.Config) error {
	opts, err := LoadInstallOptions(cfg)
	if err != nil {
		return err
	}

	// Remove old shortcuts first.
	startMenuLnk := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper.lnk")
	desktopLnk := filepath.Join(os.Getenv("USERPROFILE"), "Desktop", "Syntee Helper.lnk")
	_ = os.Remove(startMenuLnk)
	_ = os.Remove(desktopLnk)

	// Remove old autostart pointing to launcher.
	k, openErr := registry.OpenKey(registry.CURRENT_USER, autostartRegPath, registry.SET_VALUE)
	if openErr == nil {
		_ = k.DeleteValue("SynteeHelper")
		k.Close()
	}

	return registerApp(cfg, opts)
}

// ── registerApp: shortcuts + autostart + uninstall registry ───────────────

const (
	appName          = "Syntee Helper"
	appPublisher     = "Syntee"
	uninstallRegPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\SynteeHelper`
	autostartRegPath = `Software\Microsoft\Windows\CurrentVersion\Run`
)

func registerApp(cfg *config.Config, opts InstallOptions) error {
	launcherPath := filepath.Join(cfg.Dir(), "syntee-launcher.exe")
	clientPath := cfg.ClientExePath()

	// Shortcuts → client (not launcher). The launcher is only for install/update.
	if opts.StartMenuShortcut {
		if err := createShortcut(
			filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper.lnk"),
			clientPath, launcherPath,
		); err != nil {
			log.Printf("start menu shortcut: %v", err)
		}
	}
	if opts.DesktopShortcut {
		if desktop := os.Getenv("USERPROFILE"); desktop != "" {
			if err := createShortcut(
				filepath.Join(desktop, "Desktop", "Syntee Helper.lnk"),
				clientPath, launcherPath,
			); err != nil {
				log.Printf("desktop shortcut: %v", err)
			}
		}
	}

	// Autostart: HKCU\...\Run → client.
	if opts.Autostart {
		if err := setAutostart(clientPath); err != nil {
			log.Printf("autostart: %v", err)
		}
	}

	// Uninstall entry in "Programs and Features" (always created, points to launcher).
	if err := registerUninstall(cfg, launcherPath, launcherPath); err != nil {
		log.Printf("uninstall registry: %v", err)
	}

	return nil
}

// shortcutTimeout bounds the PowerShell round-trip used to create a .lnk.
// Without it a hung powershell.exe (AV script scanning, AppLocker, broken
// profile) blocks the caller forever, which used to freeze the launcher on
// "Проверка обновлений…" before the update check even started.
const shortcutTimeout = 10 * time.Second

// createShortcut creates a Windows .lnk file pointing at target, using the
// launcher exe as the brand icon. The target remains the client exe.
// We shell out to PowerShell because Go has no native
// .lnk support and pulling in a COM binding for one call is overkill.
func createShortcut(lnkPath, targetPath, iconPath string) error {
	_ = os.MkdirAll(filepath.Dir(lnkPath), 0o755)
	// Single quotes are PowerShell string delimiters; double them so a path
	// containing an apostrophe cannot break (or inject into) the script.
	script := fmt.Sprintf(
		`$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.IconLocation = '%s,0'; $s.WorkingDirectory = '%s'; $s.Save()`,
		psQuote(lnkPath), psQuote(targetPath), psQuote(iconPath), psQuote(filepath.Dir(targetPath)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), shortcutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	cmd.WaitDelay = 2 * time.Second // don't hang on inherited handles after kill
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("create shortcut %s: powershell timed out after %s", filepath.Base(lnkPath), shortcutTimeout)
		}
		return fmt.Errorf("create shortcut %s: %w", filepath.Base(lnkPath), err)
	}
	return nil
}

// psQuote escapes a value for use inside a single-quoted PowerShell literal.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// setAutostart writes the HKCU\...\Run entry so the client starts with Windows.
func setAutostart(launcherPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRegPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open autostart key: %w", err)
	}
	defer k.Close()
	return k.SetStringValue("SynteeHelper", launcherPath)
}

// registerUninstall creates the registry entry that makes the app appear in
// "Programs and Features" (aka Add/Remove Programs). The uninstall command
// points back at the launcher with --uninstall.
func registerUninstall(cfg *config.Config, launcherPath, iconPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallRegPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("create uninstall key: %w", err)
	}
	defer k.Close()

	uninstallCmd := fmt.Sprintf(`"%s" --uninstall`, launcherPath)
	installLocation := cfg.Dir()

	entries := map[string]string{
		"DisplayName":          appName,
		"DisplayVersion":       cfg.CurrentVersion,
		"Publisher":            appPublisher,
		"InstallLocation":      installLocation,
		"DisplayIcon":          iconPath + ",0",
		"UninstallString":      uninstallCmd,
		"QuietUninstallString": uninstallCmd + " --quiet",
	}
	for name, value := range entries {
		if err := k.SetStringValue(name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	// NoModify and NoRepair are DWORDs that disable the "Change" and "Repair"
	// buttons in Add/Remove Programs — we only support uninstall.
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	return nil
}
