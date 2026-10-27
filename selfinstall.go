package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/setup"
)

// selfInstall copies the launcher binary into the install directory
// (%LOCALAPPDATA%\Programs\Syntee Helper\), creates an initial launcher.json
// with the baked-in DefaultUpdateServer, and relaunches itself from there.
//
// This is called when the launcher detects it's NOT running from the install
// dir — e.g. the user downloaded syntee-launcher.exe to Downloads and
// double-clicked it. After self-install, the original download can be deleted.
//
// Returns (relaunched, err). A non-nil err means the launcher could not be
// installed: continuing from the download location would leave a second copy
// running from an unexpected directory while holding the install mutex, which
// looks to the user like a phantom "another installer is running".
func selfInstall() (bool, error) {
	if config.IsInstalled() {
		return false, nil // already in install dir — nothing to do
	}

	installDir := config.InstallDir()
	exePath, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("не удалось определить путь к установщику: %w", err)
	}

	// Create install directory.
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return false, fmt.Errorf("не удалось создать %s: %w", installDir, err)
	}

	// Copy ourselves into the install dir.
	dst := filepath.Join(installDir, "syntee-launcher.exe")
	if err := copySelf(exePath, dst); err != nil {
		return false, fmt.Errorf("не удалось скопировать установщик в %s: %w", installDir, err)
	}
	log.Printf("self-install: copied %s -> %s", exePath, dst)

	// Write launcher.json with default settings if it doesn't exist.
	cfgPath := filepath.Join(installDir, "launcher.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		defaultJSON := fmt.Sprintf(`{
  "update_server": %q,
  "channel": "stable",
  "current_version": "0.0.0",
  "pinned_version": null,
  "bad_versions": [],
  "crash_count": 0,
  "max_crash_retries": 3,
  "check_timeout_sec": 5,
  "download_timeout_sec": 0,
  "client_exe": "Syntee Helper.exe",
  "ready_marker": ".ready"
}
`, config.DefaultUpdateServer)
		if err := os.WriteFile(cfgPath, []byte(defaultJSON), 0o644); err != nil {
			return false, fmt.Errorf("не удалось записать %s: %w", cfgPath, err)
		}
		log.Printf("self-install: wrote launcher.json")
	}

	// Relaunch from install dir. If this fails the exe is still installed, so
	// this copy can keep running: next launch will pick up the installed one.
	if !relaunchFromInstall(dst) {
		log.Printf("self-install: relaunch failed, continuing from current location")
		return false, nil
	}
	return true, nil
}

func copySelf(src, dst string) error {
	return setup.CopyFileAtomic(src, dst)
}

// relaunchFromInstall starts a new copy of the launcher from the install dir
// and exits the current (temporary) process. Returns false if the relaunch
// failed, so the caller can continue running from the current location.
func relaunchFromInstall(installedPath string) bool {
	attr := &os.ProcAttr{
		Files: []*os.File{nil, nil, nil},
	}
	proc, err := os.StartProcess(installedPath, append([]string{installedPath}, os.Args[1:]...), attr)
	if err != nil {
		log.Printf("self-install: relaunch failed: %v", err)
		return false
	}
	log.Printf("self-install: relaunched from %s (pid=%d), exiting temp copy", installedPath, proc.Pid)
	_ = proc.Release()
	return true // main returns and releases mutex before child proceeds
}
