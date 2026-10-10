// Package runner spawns the client process, waits for it to exit, and reports
// whether it reached ready-state (crash-marker deleted by the client) or
// crashed before that.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
)

// Run launches the client and blocks until it exits. Before spawning it
// creates the crash-marker; the client is expected to delete it once it has
// finished booting (SynteeHelper does this in src/main/index.ts after
// ready-to-show).
//
// Returns nil if the client exited cleanly (exit 0), an *exec.ExitError with
// the real code otherwise. The caller decides what "crash" means: a non-zero
// exit OR the crash-marker still being present after exit.
func Run(ctx context.Context, cfg *config.Config) error {
	exe := cfg.ClientExePath()
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("client binary missing: %w", err)
	}

	// Drop any stale ready-marker from a previous run, then write the crash
	// marker so a kill -9 / power loss is detectable on next start.
	_ = os.Remove(cfg.ReadyMarkerPath())
	if err := os.WriteFile(cfg.CrashMarkerPath(), []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		return fmt.Errorf("write crash marker: %w", err)
	}

	cmd := exec.Command(exe)
	cmd.Dir = cfg.CurrentDir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Detach stdin so the client doesn't inherit the launcher's console
	// (we run with -H windowsgui, so there's no console anyway, but this
	// keeps behaviour consistent when launched from a terminal).
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start client: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ReachedReady reports whether the client deleted its crash-marker during the
// last run — i.e. it booted successfully. Called after Run returns.
func ReachedReady(cfg *config.Config) bool {
	_, err := os.Stat(cfg.CrashMarkerPath())
	if err == nil {
		// Marker still present → client crashed or was killed before ready.
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	// Stat error for some other reason — be conservative, treat as not-ready.
	return false
}

// MarkReady deletes the crash-marker. This is a no-op when the marker is
// already gone (client called it) but lets the launcher clear a stale marker
// after a successful manual launch outside the normal flow.
func MarkReady(cfg *config.Config) {
	_ = os.Remove(cfg.CrashMarkerPath())
}

// WaitForReady blocks until the ready-marker appears or timeout elapses. The
// launcher uses this in the non-force flow to decide whether to keep the
// crash-counter (client crashed before ready) or reset it (client ready).
//
// SynteeHelper writes the marker in src/main/index.ts after the main window
// has shown; we wait up to 60s which is generous for cold starts on slow VMs.
func WaitForReady(cfg *config.Config, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(cfg.ReadyMarkerPath()); err == nil {
			return true
		}
		if _, err := os.Stat(cfg.CrashMarkerPath()); os.IsNotExist(err) {
			// Client deleted the crash-marker — equivalent to ready.
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// StopClient sends a graceful termination to a running client by writing a
// "quit" file into current/. SynteeHelper watches for this file and exits
// cleanly so the launcher can apply a pending background update. If the file
// signal isn't honoured within `timeout`, the process is killed.
//
// (This is an optional helper; the primary quit path is the client itself
// calling app.quit() when the user accepts an update.)
func StopClient(cfg *config.Config, pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	quitFile := filepath.Join(cfg.CurrentDir(), ".launcher_quit")
	_ = os.WriteFile(quitFile, []byte("quit"), 0o644)
	defer os.Remove(quitFile)

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil // already gone
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return proc.Kill()
}
