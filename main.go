// Command syntee-launcher is the bootstrap updater for SynteeHelper.
//
// It is the single entry point for the desktop install: shortcuts and the Run
// registry key point at this binary, never at SynteeHelper.exe. On every
// launch it (1) checks for crash-loop recovery, (2) asks the gateway whether
// a newer build exists for its channel, (3) applies the update if one is
// available (blocking when force, background when not), and (4) spawns the
// client and waits for it to exit before looping.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/rollback"
	"github.com/Lun13371/syntee-launcher/internal/runner"
	"github.com/Lun13371/syntee-launcher/internal/ui"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

// jsonMarshal / jsonUnmarshal are thin wrappers so main.go doesn't import
// encoding/json directly in multiple places — keeps the imports tidy.
func jsonMarshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func main() {
	// The launcher runs with -H windowsgui so there's no console. Route logs
	// to a file next to the binary so crashes are diagnosable.
	initLogging()
	defer logSync()

	log.Printf("syntee-launcher starting (pid=%d)", os.Getpid())

	cfg, err := config.Load()
	if err != nil {
		ui.ShowError("Syntee Launcher", "Failed to read launcher.json: "+err.Error())
		os.Exit(1)
	}

	// Step 1: crash-loop recovery. If the current version is in bad_versions,
	// restore the most recent backup before doing anything else.
	if restored, rerr := rollback.RestoreIfBad(cfg); rerr != nil {
		log.Printf("rollback failed: %v", rerr)
		ui.ShowError("Syntee Launcher",
			fmt.Sprintf("The current version (%s) is marked as broken and no backup could be restored.\n\nPlease reinstall SynteeHelper manually.", cfg.CurrentVersion))
		os.Exit(1)
	} else if restored != "" {
		log.Printf("restored previous version %s after crash-loop", restored)
		if err := cfg.Save(); err != nil {
			log.Printf("save config after restore: %v", err)
		}
	}

	// Step 2: check for an update. Offline-first — a failed check never
	// blocks the client from starting.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	client := updater.NewClient(cfg)
	manifest, err := client.Check(ctx, cfg.Channel, cfg.CurrentVersion)
	if err != nil {
		log.Printf("update check failed (offline?): %v — starting current version", err)
	} else if manifest != nil && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
		log.Printf("update available: %s -> %s (force=%v)", cfg.CurrentVersion, manifest.Version, manifest.Force)
		if manifest.Force {
			// Blocking update: show progress, download, apply, then start
			// the freshly-installed version.
			if err := doForcedUpdate(ctx, cfg, client, manifest); err != nil {
				log.Printf("forced update failed: %v — falling back to current", err)
				// Don't exit: start the current build so the user isn't stuck.
			}
		} else {
			// Non-force: try a background download into staging/ so the next
			// launch can apply it instantly. Failure here is non-fatal.
			if err := tryBackgroundDownload(ctx, cfg, client, manifest); err != nil {
				log.Printf("background download failed (will retry next launch): %v", err)
			}
		}
	} else {
		log.Printf("no update available (current=%s)", cfg.CurrentVersion)
	}

	// Step 3: apply any staging update that was prepared during a previous
	// non-force run (zip already verified in staging).
	applyStagingIfReady(cfg)

	// Step 4: spawn the client.
	for {
		if err := runClientOnce(ctx, cfg); err != nil {
			log.Printf("client run error: %v", err)
		}
		// Loop: after the client exits, check for updates again (the user may
		// have accepted an update inside the app → app.quit() → we loop).
		// A short delay prevents a tight spin loop if the client keeps dying.
		time.Sleep(1 * time.Second)

		// Re-check for an update before relaunching.
		manifest, err := client.Check(ctx, cfg.Channel, cfg.CurrentVersion)
		if err == nil && manifest != nil && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
			if manifest.Force {
				if err := doForcedUpdate(ctx, cfg, client, manifest); err != nil {
					log.Printf("forced update (relaunch): %v", err)
				}
			} else {
				_ = tryBackgroundDownload(ctx, cfg, client, manifest)
				applyStagingIfReady(cfg)
			}
		}
	}
}

// runClientOnce spawns the client, waits for it to exit, and updates the
// crash counter based on whether it reached ready-state.
func runClientOnce(ctx context.Context, cfg *config.Config) error {
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	runErr := runner.Run(runCtx, cfg)
	if runErr != nil {
		log.Printf("client exited with error: %v", runErr)
	}

	// Did the client reach ready-state?
	if runner.ReachedReady(cfg) {
		cfg.ResetCrashCount()
		runner.MarkReady(cfg)
		if err := cfg.Save(); err != nil {
			log.Printf("save config after ready: %v", err)
		}
		return runErr
	}

	// Crash before ready — bump the counter.
	count := cfg.IncrementCrashCount()
	log.Printf("client crashed before ready (count=%d/%d)", count, cfg.MaxCrashRetries)
	if err := cfg.Save(); err != nil {
		log.Printf("save config after crash: %v", err)
	}

	if count >= cfg.MaxCrashRetries {
		log.Printf("crash limit reached — marking %s as bad and restoring backup", cfg.CurrentVersion)
		cfg.AddBadVersion(cfg.CurrentVersion)
		if restored, rerr := rollback.RestoreIfBad(cfg); rerr != nil {
			log.Printf("restore after crash-loop: %v", rerr)
			ui.ShowError("Syntee Launcher",
				fmt.Sprintf("SynteeHelper (%s) keeps crashing and no backup is available.\n\nPlease reinstall manually.", cfg.CurrentVersion))
			os.Exit(1)
		} else if restored != "" {
			log.Printf("restored %s after crash-loop", restored)
		}
		if err := cfg.Save(); err != nil {
			log.Printf("save config after restore: %v", err)
		}
	}
	return runErr
}

// doForcedUpdate downloads and applies an update synchronously, showing a
// progress dialog to the user.
func doForcedUpdate(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest) error {
	ui.ShowForceProgress("Downloading required update " + m.Version + "…")
	defer ui.DismissForceProgress()

	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), func(done, total int64) {
		ui.ShowForceProgress("Downloading required update " + m.Version + "…\n" + ui.FormatPercent(done, total))
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("downloaded %s in %s (sha256 ok)", res.Path, res.Duration)

	if err := updater.Apply(res.Path, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	cfg.SetCurrentVersion(m.Version)
	if err := cfg.Save(); err != nil {
		log.Printf("save config after apply: %v", err)
	}
	// Remove the staged manifest so a later applyStagingIfReady doesn't redo it.
	_ = os.Remove(cfg.StagingDir() + string(os.PathSeparator) + "update.json")
	log.Printf("applied update -> %s", m.Version)
	return nil
}

// tryBackgroundDownload fetches the zip into staging/ without applying it.
// The next launch (or an app.quit() from the in-app updater) picks it up via
// applyStagingIfReady. The manifest is cached alongside the zip so the applier
// can bump current_version and verify integrity without re-querying the server.
func tryBackgroundDownload(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest) error {
	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), nil)
	if err != nil {
		return err
	}
	// Persist the manifest next to the zip so applyStagingIfReady can pick it
	// up on the next launch without a network round-trip.
	manifestPath := cfg.StagingDir() + string(os.PathSeparator) + "update.json"
	manifestJSON, _ := jsonMarshal(m)
	_ = os.WriteFile(manifestPath, manifestJSON, 0o644)
	log.Printf("background download ready: %s (sha256 verified, manifest cached)", res.Path)
	return nil
}

// applyStagingIfReady checks for a verified staging/update.zip and applies it.
// This covers the non-force flow: previous launch downloaded in the
// background, this launch swaps it in. Requires staging/update.json to be
// present alongside the zip — without it we can't know the target version
// and we skip (the next check will re-download).
func applyStagingIfReady(cfg *config.Config) {
	zipPath := cfg.StagingDir() + string(os.PathSeparator) + "update.zip"
	manifestPath := cfg.StagingDir() + string(os.PathSeparator) + "update.json"

	info, err := os.Stat(zipPath)
	if err != nil || info.Size() == 0 {
		return
	}
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		log.Printf("staged zip present but update.json missing — skipping apply")
		return
	}
	var m updater.Manifest
	if err := jsonUnmarshal(manifestRaw, &m); err != nil {
		log.Printf("parse staged manifest: %v — removing stale staging", err)
		_ = os.Remove(zipPath)
		_ = os.Remove(manifestPath)
		return
	}
	// If the staged version is already current, the previous launch applied
	// it but didn't clean up staging — drop the stale zip.
	if m.Version == cfg.CurrentVersion {
		_ = os.Remove(zipPath)
		_ = os.Remove(manifestPath)
		return
	}

	log.Printf("applying staged update %s -> %s from %s", cfg.CurrentVersion, m.Version, zipPath)
	if err := updater.Apply(zipPath, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion); err != nil {
		log.Printf("apply staging failed: %v — removing stale zip", err)
		_ = os.Remove(zipPath)
		_ = os.Remove(manifestPath)
		return
	}
	cfg.SetCurrentVersion(m.Version)
	if err := cfg.Save(); err != nil {
		log.Printf("save config after staged apply: %v", err)
	}
	_ = os.Remove(zipPath)
	_ = os.Remove(manifestPath)
	log.Printf("staged update applied -> %s", m.Version)
}

func handleSignals(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Printf("signal received, cancelling")
	cancel()
}

// --- logging to file ---

var logFile *os.File

func initLogging() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	f, err := os.OpenFile(exe[:len(exe)-len(".exe")]+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Fall back to no logging — better than crashing on a permission issue.
		log.SetOutput(os.NewFile(0, os.DevNull))
		return
	}
	logFile = f
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func logSync() {
	if logFile != nil {
		_ = logFile.Sync()
		_ = logFile.Close()
	}
}
