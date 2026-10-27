package setup

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"context"
	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/runner"
	"github.com/Lun13371/syntee-launcher/internal/ui"
	"golang.org/x/sys/windows/registry"
	"time"
)

// Uninstall removes every trace of Syntee Helper from the system:
//
//  1. Shortcuts (Start Menu, Desktop)
//  2. Autostart registry key
//  3. Uninstall registry entry ("Programs and Features")
//  4. Client files: current/, staging/, launcher.json, logs
//  5. The launcher binary itself (scheduled via a detached cmd process
//     that waits 2 seconds then deletes the exe — we can't delete ourselves
//     while running).
//
// The --quiet flag suppresses the confirmation dialog (used by
// "QuietUninstallString" in the registry).
func Uninstall(cfg *config.Config, quiet bool) {
	if !quiet {
		ui.ShowError("Syntee Helper — удаление",
			"Syntee Helper будет полностью удалён.\n\n"+
				"Все файлы программы будут удалены, включая настройки.\n"+
				"Нажмите OK для продолжения.")
	}

	log.Printf("uninstall: starting")
	if err := runner.StopProcessesIn(context.Background(), cfg.CurrentDir(), 2*time.Second); err != nil {
		log.Printf("uninstall: stop client: %v", err)
		return
	}

	// 1. Remove shortcuts.
	removeShortcuts()
	log.Printf("uninstall: shortcuts removed")

	// 2. Remove autostart.
	removeAutostart()
	log.Printf("uninstall: autostart removed")

	// 3. Remove uninstall registry entry.
	registry.DeleteKey(registry.CURRENT_USER, uninstallRegPath)
	log.Printf("uninstall: registry entry removed")

	// 4. Remove client files.
	dirsToRemove := []string{
		cfg.CurrentDir(),
		filepath.Join(cfg.Dir(), "backups"), // remove data left by older launcher versions
		cfg.StagingDir(),
	}
	filesToRemove := []string{
		filepath.Join(cfg.Dir(), "launcher.json"),
		filepath.Join(cfg.Dir(), "syntee-launcher.log"),
		filepath.Join(cfg.Dir(), ".setup_done"),
		filepath.Join(cfg.Dir(), "install-options.json"),
		filepath.Join(cfg.Dir(), "syntee-launcher.log.1"),
		filepath.Join(cfg.Dir(), "syntee-launcher.log.2"),
	}
	for _, d := range dirsToRemove {
		if err := os.RemoveAll(d); err != nil {
			log.Printf("uninstall: remove %s: %v", d, err)
		}
	}
	for _, f := range filesToRemove {
		_ = os.Remove(f)
	}
	log.Printf("uninstall: files removed")

	// 5. Self-delete: spawn a detached cmd that waits 2 seconds then deletes
	// the launcher exe and its log. We can't delete ourselves while running,
	// so we schedule it post-exit.
	launcherPath, _ := os.Executable()
	if launcherPath != "" {
		selfDelete(launcherPath)
	}

	if !quiet {
		ui.ShowError("Syntee Helper — удаление завершено",
			"Syntee Helper успешно удалён.")
	}

	log.Printf("uninstall: done, exiting")
	os.Exit(0)
}

func removeShortcuts() {
	startMenu := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper.lnk")
	_ = os.Remove(startMenu)
	// Remove the Start Menu folder too (in case other files are there).
	startMenuDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Syntee Helper")
	_ = os.Remove(startMenuDir)

	if desktop := os.Getenv("USERPROFILE"); desktop != "" {
		_ = os.Remove(filepath.Join(desktop, "Desktop", "Syntee Helper.lnk"))
	}
}

func removeAutostart() {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRegPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.DeleteValue("SynteeHelper")
}

// selfDelete spawns a detached cmd.exe that:
//  1. Waits 2 seconds (lets the launcher process exit).
//  2. Deletes the launcher .exe and .log.
//  3. Deletes the install directory if empty.
//
// The cmd window is hidden (CREATE_NO_WINDOW) so the user sees nothing.
func selfDelete(exePath string) {
	dir := filepath.Dir(exePath)
	logPath := strings.TrimSuffix(exePath, ".exe") + ".log"

	// cmd /c (ping 127.0.0.1 -n 3 >nul) & del "exe" & del "log" & rmdir "dir" 2>nul
	script := fmt.Sprintf(
		`ping 127.0.0.1 -n 3 >nul & del "%s" & del "%s" & rmdir "%s" 2>nul`,
		exePath, logPath, dir,
	)

	cmd := exec.Command("cmd", "/c", script)
	// Detach: hide window, don't inherit handles.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
	_ = cmd.Start()
}
