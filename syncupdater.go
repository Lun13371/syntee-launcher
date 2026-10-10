package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/setup"
)

// syncUpdaterExe copies the running launcher binary into current/updater.exe
// on every launch (as long as the client is installed), so the client's
// quit-and-install flow always finds a fresh updater next to the client.
//
// Previously updater.exe was only refreshed after a successful update apply
// (copyUpdaterAfterApply): a newer launcher downloaded from the website never
// replaced a stale updater.exe until an update happened to ship.
func syncUpdaterExe(cfg *config.Config) {
	// Nothing to sync into when the client isn't installed yet — the first-run
	// setup flow calls copyUpdaterAfterApply once the client exists.
	if _, err := os.Stat(cfg.ClientExePath()); err != nil {
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		log.Printf("syncUpdater: exe path: %v", err)
		return
	}

	dst := filepath.Join(cfg.CurrentDir(), "updater.exe")

	// Don't copy onto ourselves (e.g. the client spawned current/updater.exe
	// and the self-install relaunch failed, so we kept running from there).
	if strings.EqualFold(filepath.Clean(exePath), filepath.Clean(dst)) {
		return
	}

	if err := copyFileAtomic(exePath, dst); err != nil {
		log.Printf("syncUpdater: copy -> %s: %v", dst, err)
		return
	}
	log.Printf("syncUpdater: synced -> %s", dst)
}

// copyFileAtomic preserves the prior updater on failure and skips identical bytes.
func copyFileAtomic(src, dst string) error { return setup.CopyFileAtomic(src, dst) }
