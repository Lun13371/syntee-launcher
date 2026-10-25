// Command syntee-launcher is the installer and updater for SynteeHelper.
//
// The user downloads this binary (7 MB) from the gateway, runs it, and it
// self-installs to %LOCALAPPDATA%\Programs\Syntee Helper\. It then downloads
// the full client zip (~400 MB), extracts it, creates shortcuts pointing at
// the client exe, and launches it.
//
// Shortcuts, autostart and the uninstall registry entry point at the client
// ("Syntee Helper.exe"), not at this launcher. The launcher is only run
// explicitly by the user to install, update, or repair.
//
// The launcher has two UI modes:
//   - GUI mode (default): a WebView2-based glassmorphism window with animated
//     progress, changelog display, and user decision buttons.
//   - Headless mode (fallback): the old MessageBox-based UI, used when
//     WebView2 is unavailable.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/gui"
	"github.com/Lun13371/syntee-launcher/internal/rollback"
	"github.com/Lun13371/syntee-launcher/internal/runner"
	"github.com/Lun13371/syntee-launcher/internal/setup"
	"github.com/Lun13371/syntee-launcher/internal/ui"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

func jsonMarshal(v any) ([]byte, error)      { return json.MarshalIndent(v, "", "  ") }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func main() {
	// Waiting instances must not hold current/ as their working directory.
	chdirToTemp()
	release, err := setup.AcquireInstallLock(config.InstallDir())
	if err != nil {
		ui.ShowError("Syntee Helper", err.Error())
		return
	}
	defer release()
	initLogging()
	defer logSync()

	log.Printf("syntee-launcher starting (pid=%d, args=%v)", os.Getpid(), os.Args[1:])

	// Release any inherited working directory that points inside the install
	// tree. The client spawns us (current/updater.exe) with cwd inherited
	// from itself — and the client runs with cwd=current/ (we set cmd.Dir
	// when launching it). Windows refuses to rename a directory that is any
	// process's CWD, so as long as we — or the WebView2 helper processes the
	// GUI spawns later, which inherit our CWD at creation time — sit in
	// current/, the apply step can never rename it ("being used by another
	// process"). Chdir to a neutral location BEFORE creating the GUI.
	chdirToTemp()

	// --uninstall: no GUI, use MessageBox.
	for _, arg := range os.Args[1:] {
		if arg == "--uninstall" || arg == "-uninstall" {
			cfg, err := config.Load()
			if err != nil {
				ui.ShowError("Syntee Helper", "Не удалось прочитать конфигурацию: "+err.Error())
				os.Exit(1)
			}
			quiet := false
			for _, a := range os.Args[1:] {
				if a == "--quiet" || a == "-quiet" {
					quiet = true
				}
			}
			setup.Uninstall(cfg, quiet)
			return
		}
	}

	// Self-install: if not running from the install dir, copy ourselves
	// to %LOCALAPPDATA%\Programs\Syntee Helper\ and relaunch from there.
	// This makes the launcher behave like a normal installer when the user
	// downloads it to Downloads and double-clicks.
	if selfInstall() {
		return // relaunch happened, exit this temp copy
	}

	cfg, err := config.Load()
	if err != nil {
		ui.ShowError("Syntee Launcher", "Failed to read launcher.json: "+err.Error())
		os.Exit(1)
	}

	// Migrate a stale update server (old IP / old domain) to the baked-in
	// default. launcher.json wins over DefaultUpdateServer, so installs made
	// before the domain migration would poll the dead IP forever without this.
	if cfg.UpdateServer != config.DefaultUpdateServer {
		log.Printf("migrating update_server %q -> %q", cfg.UpdateServer, config.DefaultUpdateServer)
		cfg.UpdateServer = config.DefaultUpdateServer
		if err := cfg.Save(); err != nil {
			log.Printf("save config after update_server migration: %v", err)
		}
	}

	// Resolve an interrupted swap before any current/-dependent operation.
	if err := setup.RecoverInstallation(cfg); err != nil {
		ui.ShowError("Syntee Helper", err.Error())
		return
	}
	if cfg.IsBad(cfg.CurrentVersion) {
		if err := runner.StopProcessesIn(context.Background(), cfg.CurrentDir(), 2*time.Second); err != nil {
			ui.ShowError("Syntee Helper", err.Error())
			return
		}
		if _, err := rollback.RestoreIfBad(cfg); err != nil {
			ui.ShowError("Syntee Helper", err.Error())
			return
		}
		if err := cfg.Save(); err != nil {
			ui.ShowError("Syntee Helper", err.Error())
			return
		}
	}

	// Keep current/updater.exe in sync with this binary on EVERY launch — the
	// client's quit-and-install spawns updater.exe, so it must be the freshest
	// launcher build, not whatever happened to apply the last update.
	syncUpdaterExe(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	client := updater.NewClient(cfg)

	// Try GUI mode. If WebView2 is unavailable and can't be installed,
	// fall back to the headless MessageBox UI.
	g, err := gui.New()
	if err != nil {
		log.Printf("GUI init failed, falling back to headless: %v", err)
		runHeadless(ctx, cfg, client)
		return
	}

	g.SetVerifyConfig(cfg)

	g.RunContext(ctx, func(guiCtx context.Context) { runLifecycle(guiCtx, cfg, client, g) })
}

// runLifecycle is the GUI-mode lifecycle. It runs in a goroutine alongside
// the webview message loop. State changes are pushed to the frontend via
// g.SetState / g.SetProgress.
func runLifecycle(ctx context.Context, cfg *config.Config, client *updater.Client, g *gui.GUI) {
	// ── 1. First-run setup ──────────────────────────────────────────────
	needsInstall := false
	if _, err := os.Stat(cfg.ClientExePath()); err != nil {
		needsInstall = true
	}

	if needsInstall {
		// Show welcome screen with install options; wait for user to click "Install".
		g.SetState(gui.StateWelcome, &gui.Payload{
			Title: cfg.Dir(),
		})
		installOpts, ok := waitChannel(ctx, g.InstallCh())
		if !ok {
			return
		}
		log.Printf("install options: desktop=%v startmenu=%v autostart=%v",
			installOpts.DesktopShortcut, installOpts.StartMenuShortcut, installOpts.Autostart)

		g.SetState(gui.StateDownloading, &gui.Payload{
			Title:      "Установка Syntee Helper",
			IsFirstRun: true,
		})
		g.ResetSpeed()

		for {
			err := setup.RunIfNeededWithOptions(ctx, cfg, client, setup.InstallOptions{
				DesktopShortcut:   installOpts.DesktopShortcut,
				StartMenuShortcut: installOpts.StartMenuShortcut,
				Autostart:         installOpts.Autostart,
			},
				g.SetProgress,
				func(phase string) {
					switch phase {
					case "download":
					case "extract":
						g.SetState(gui.StateExtracting, nil)
						g.ResetSpeed()
					}
				},
			)
			if err != nil {
				log.Printf("first-run setup failed: %v", err)
				g.SetState(gui.StateError, &gui.Payload{
					ErrorMessage: "Не удалось загрузить клиент с сервера.\n\n" +
						"Проверьте подключение к интернету.\n\n" +
						"Ошибка: " + err.Error(),
				})
				if _, ok := waitChannel(ctx, g.RetryCh()); !ok {
					return
				}
				g.ResetSpeed()
				g.SetState(gui.StateDownloading, &gui.Payload{IsFirstRun: true})
				continue
			}
			break
		}

		// Installation complete — launch client with countdown.
		launchClientAndCountdown(ctx, cfg, g)
		return
	}

	// ── 2. Already installed: check for updates ─────────────────────────
	// Put a state on screen before any blocking work: everything below runs
	// off the UI thread, and without this the window would sit on the boot
	// placeholder with no feedback about what is happening.
	g.SetState(gui.StateChecking, nil)

	// Ensure shortcuts exist (they might be missing if a previous install
	// was done before the shortcut fix, or if the user deleted them).
	// Runs in the background: createShortcut shells out to PowerShell, which
	// can block for a long time behind AV/AppLocker policy, and shortcuts are
	// cosmetic for the update flow. A blocking repair here used to look
	// exactly like a hung update check. It works on a freshly loaded config so
	// it never shares state with the version/crash-count mutations below.
	go func() {
		repairCfg, err := config.Load()
		if err != nil {
			log.Printf("ensure shortcuts: reload config: %v", err)
			return
		}
		if err := setup.EnsureShortcuts(repairCfg); err != nil {
			log.Printf("ensure shortcuts: %v", err)
		}
	}()

	// Crash-loop recovery: if current version is marked bad, restore backup.
	if restored, rerr := rollback.RestoreIfBad(cfg); rerr != nil {
		log.Printf("rollback failed: %v", rerr)
	} else if restored != "" {
		log.Printf("restored previous version %s after crash-loop", restored)
		if err := cfg.Save(); err != nil {
			log.Printf("save config after restore: %v", err)
		}
	}

	manifest, err := client.Check(ctx, cfg.Channel, cfg.CurrentVersion)
	if err != nil {
		log.Printf("update check failed: %v", err)
	}

	if err == nil && manifest != nil && !cfg.IsBad(manifest.Version) && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
		log.Printf("update available: %s -> %s (force=%v)", cfg.CurrentVersion, manifest.Version, manifest.Force)
		if manifest.Force {
			g.SetState(gui.StateForcedUpdate, &gui.Payload{
				Version: manifest.Version,
				Force:   true,
			})
			g.ResetSpeed()
			if !updateWithRetry(ctx, cfg, client, manifest, g) {
				return
			}
		} else {
			g.SetState(gui.StateUpdateAvailable, &gui.Payload{
				Version:   manifest.Version,
				Changelog: manifest.Changelog,
			})
			decision, ok := waitChannel(ctx, g.DecisionCh())
			if !ok {
				return
			}
			if decision {
				g.SetState(gui.StateDownloading, &gui.Payload{
					Version: manifest.Version,
				})
				g.ResetSpeed()
				if !updateWithRetry(ctx, cfg, client, manifest, g) {
					return
				}
			} else {
				// User declined update — show installed screen.
				showInstalledScreen(ctx, cfg, client, g)
				return
			}
		}

		// Update complete — launch client with countdown.
		launchClientAndCountdown(ctx, cfg, g)
		return
	}

	log.Printf("no update available (current=%s)", cfg.CurrentVersion)
	showInstalledScreen(ctx, cfg, client, g)
}

// showInstalledScreen displays the "already installed" management screen and
// handles reinstall requests.
func showInstalledScreen(ctx context.Context, cfg *config.Config, client *updater.Client, g *gui.GUI) {
	g.SetState(gui.StateInstalled, &gui.Payload{
		Title:   cfg.Dir(),
		Version: cfg.CurrentVersion,
	})

	for {
		select {
		case <-g.LaunchCh():
			launchClientAndCountdown(ctx, cfg, g)
			return
		case <-ctx.Done():
			return
		case <-g.ReinstallCh():
			for {
				g.SetState(gui.StateDownloading, &gui.Payload{Title: "Переустановка Syntee Helper", IsFirstRun: true})
				manifest, err := client.Check(ctx, cfg.Channel, "0.0.0")
				if err == nil && manifest == nil {
					err = fmt.Errorf("no release available")
				}
				if err != nil {
					g.SetState(gui.StateError, &gui.Payload{ErrorMessage: err.Error()})
					if _, ok := waitChannel(ctx, g.RetryCh()); !ok {
						return
					}
					continue
				}
				if !updateWithRetry(ctx, cfg, client, manifest, g) {
					return
				}
				if err := setup.RecreateShortcuts(cfg); err != nil {
					log.Printf("reinstall shortcuts: %v", err)
				}
				launchClientAndCountdown(ctx, cfg, g)
				return
			}
		}
	}
}

// launchClientDetached starts the client process detached so it survives
// launcher exit. Used by the headless fallback (no GUI countdown).
// Full monitoring of direct shortcuts requires client cooperation: maintain
// readiness/crash markers and invoke updater/recovery after repeated failures.
func launchClientDetached(ctx context.Context, cfg *config.Config) {
	if err := launchAndMonitor(ctx, cfg); err != nil {
		log.Printf("client startup: %v", err)
	}
}

func launchClientAndCountdown(ctx context.Context, cfg *config.Config, g *gui.GUI) {
	for {
		g.SetState(gui.StateLaunching, nil)
		if err := launchAndMonitor(ctx, cfg); err != nil {
			if ctx.Err() != nil {
				return
			}
			g.SetState(gui.StateError, &gui.Payload{ErrorMessage: "Не удалось запустить клиент: " + err.Error()})
			if _, ok := waitChannel(ctx, g.RetryCh()); !ok {
				return
			}
			continue
		}
		for i := 10; i > 0; i-- {
			g.SetCountdown(i)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		g.Terminate()
		return
	}
}

func launchAndMonitor(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := runner.ProcessesIn(cfg.CurrentDir())
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if err := os.Remove(cfg.ReadyMarkerPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(cfg.CrashMarkerPath(), []byte(time.Now().Format(time.RFC3339Nano)), 0644); err != nil {
		return err
	}
	cmd := exec.Command(cfg.ClientExePath()) // Never CommandContext: launcher exit must not kill client.
	cmd.Dir = cfg.CurrentDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008}
	if err := cmd.Start(); err != nil {
		return recordStartupFailure(cfg, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if runner.ReachedReady(cfg) || fileExists(cfg.ReadyMarkerPath()) {
				runner.MarkReady(cfg)
				cfg.ResetCrashCount()
				return cfg.Save()
			}
		case err := <-exited:
			if runner.ReachedReady(cfg) || fileExists(cfg.ReadyMarkerPath()) {
				runner.MarkReady(cfg)
				cfg.ResetCrashCount()
				return cfg.Save()
			}
			if err == nil {
				// A clean exit can be a user action or Electron single-instance handoff.
				runner.MarkReady(cfg)
				return nil
			}
			return recordStartupFailure(cfg, fmt.Errorf("client exited before ready: %w", err))
		case <-timer.C:
			// Slow boot or absent cooperation is not proof of a crash. Leave client alive.
			log.Printf("client did not signal ready within 60 seconds; left running")
			return nil
		}
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func recordStartupFailure(cfg *config.Config, cause error) error {
	count := cfg.IncrementCrashCount()
	if count >= cfg.MaxCrashRetries {
		cfg.AddBadVersion(cfg.CurrentVersion)
		if err := runner.StopProcessesIn(context.Background(), cfg.CurrentDir(), time.Second); err != nil {
			return fmt.Errorf("%v; stop helpers: %w", cause, err)
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		if _, err := rollback.RestoreIfBad(cfg); err != nil {
			return fmt.Errorf("%v; rollback: %w", cause, err)
		}
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("%v; save: %w", cause, err)
	}
	return cause
}

func waitChannel[T any](ctx context.Context, ch <-chan T) (T, bool) {
	select {
	case <-ctx.Done():
		var zero T
		return zero, false
	case value, ok := <-ch:
		return value, ok
	}
}

func updateWithRetry(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest, g *gui.GUI) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		g.ResetSpeed()
		g.SetState(gui.StateDownloading, &gui.Payload{Version: m.Version})
		if err := doUpdate(ctx, cfg, client, m, g); err == nil {
			return true
		} else {
			if ctx.Err() != nil {
				return false
			}
			g.SetState(gui.StateError, &gui.Payload{ErrorMessage: "Ошибка обновления: " + err.Error()})
			if _, ok := waitChannel(ctx, g.RetryCh()); !ok {
				return false
			}
		}
	}
}

// chdirToTemp moves the process working directory to the OS temp dir.
// See the call site in main for why: an inherited CWD inside current/ makes
// the apply-time rename of current/ fail with a sharing violation, and the
// WebView2 children spawned by the GUI inherit our CWD when they are created,
// so this must happen before gui.New().
func chdirToTemp() {
	tmp := os.TempDir()
	if tmp == "" {
		return
	}
	if err := os.Chdir(tmp); err != nil {
		log.Printf("chdir %s: %v (continuing with inherited cwd)", tmp, err)
		return
	}
	log.Printf("chdir -> %s", tmp)
}

// killClientIfRunning only targets actual process images under current/.
func killClientIfRunning(ctx context.Context, cfg *config.Config) error {
	return runner.StopProcessesIn(ctx, cfg.CurrentDir(), 2*time.Second)
}

// doUpdate downloads and applies an update, reporting progress to the GUI.
// Used for both forced and user-accepted updates.
func doUpdate(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest, g *gui.GUI) error {
	if err := setup.ValidateRelease(cfg, m); err != nil {
		return err
	}
	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), func(done, total int64) {
		g.SetProgress(done, total)
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("downloaded %s in %s (sha256 ok)", res.Path, res.Duration)

	g.SetState(gui.StateExtracting, nil)
	g.ResetSpeed()
	if err := killClientIfRunning(ctx, cfg); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := updater.ApplyVersion(res.Path, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion, m.Version,
		func(done, total int64) { g.SetProgress(done, total) }); err != nil {
		return fmt.Errorf("apply: %w", err)
	}

	cfg.SetCurrentVersion(m.Version)
	if err := cfg.Save(); err != nil {
		log.Printf("save config after apply: %v", err)
	}
	copyUpdaterAfterApply(cfg)
	_ = os.Remove(filepath.Join(cfg.StagingDir(), "update.json"))
	log.Printf("applied update -> %s", m.Version)
	return nil
}

// copyUpdaterAfterApply copies the launcher exe into current/updater.exe
// after an update. The client uses updater.exe to self-heal if the main
// launcher is missing.
func copyUpdaterAfterApply(cfg *config.Config) {
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("copyUpdater: %v", err)
		return
	}
	dst := filepath.Join(cfg.CurrentDir(), "updater.exe")
	if err := copyFileAtomic(exePath, dst); err != nil {
		log.Printf("copyUpdater: %v", err)
		return
	}
	log.Printf("copyUpdater: copied -> %s", dst)
}

// ── Headless fallback (old MessageBox UI) ──────────────────────────────

func runHeadless(ctx context.Context, cfg *config.Config, client *updater.Client) {
	var headlessMsg string

	onProgress := func(done, total int64) {
		ui.ShowForceProgress(headlessMsg + "\n" + ui.FormatPercent(done, total))
	}
	onPhase := func(phase string) {
		switch phase {
		case "download":
			headlessMsg = "Загрузка клиента…"
			ui.ShowForceProgress(headlessMsg)
		case "extract":
			headlessMsg = "Распаковка…"
		}
	}

	if err := setup.RunIfNeeded(ctx, cfg, client, onProgress, onPhase); err != nil {
		log.Printf("first-run setup failed: %v", err)
		ui.ShowError("Syntee Helper — установка",
			"Не удалось загрузить клиент с сервера.\n\n"+
				"Проверьте подключение к интернету и попробуйте снова.\n\n"+
				"Ошибка: "+err.Error())
		os.Exit(1)
	}

	if restored, rerr := rollback.RestoreIfBad(cfg); rerr != nil {
		log.Printf("rollback failed: %v", rerr)
	} else if restored != "" {
		log.Printf("restored previous version %s after crash-loop", restored)
		if err := cfg.Save(); err != nil {
			log.Printf("save config after restore: %v", err)
		}
	}

	manifest, err := client.Check(ctx, cfg.Channel, cfg.CurrentVersion)
	if err != nil {
		log.Printf("update check failed: %v", err)
	} else if manifest != nil && !cfg.IsBad(manifest.Version) && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
		log.Printf("update available: %s -> %s (force=%v)", cfg.CurrentVersion, manifest.Version, manifest.Force)
		if manifest.Force {
			if err := doForcedUpdateHeadless(ctx, cfg, client, manifest); err != nil {
				log.Printf("forced update failed: %v", err)
			}
		}
	} else {
		log.Printf("no update available (current=%s)", cfg.CurrentVersion)
	}

	// Launch the client (detached) so the user gets the app running.
	launchClientDetached(ctx, cfg)

	log.Printf("launcher done, exiting")
}

func doForcedUpdateHeadless(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest) error {
	if err := setup.ValidateRelease(cfg, m); err != nil {
		return err
	}
	ui.ShowForceProgress("Загрузка обновления " + m.Version + "…")

	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), func(done, total int64) {
		ui.ShowForceProgress("Загрузка обновления " + m.Version + "…\n" + ui.FormatPercent(done, total))
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("downloaded %s in %s (sha256 ok)", res.Path, res.Duration)

	if err := killClientIfRunning(ctx, cfg); err != nil {
		return err
	}
	if err := updater.ApplyVersion(res.Path, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion, m.Version, nil); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	cfg.SetCurrentVersion(m.Version)
	if err := cfg.Save(); err != nil {
		log.Printf("save config after apply: %v", err)
	}
	copyUpdaterAfterApply(cfg)
	_ = os.Remove(filepath.Join(cfg.StagingDir(), "update.json"))
	log.Printf("applied update -> %s", m.Version)
	return nil
}

func handleSignals(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Printf("signal received, cancelling")
	cancel()
}

// --- logging to file ---

var logFile *setup.RotatingLog

func initLogging() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path := strings.TrimSuffix(exe, filepath.Ext(exe)) + ".log"
	f, err := setup.OpenRotatingLog(path, 4*1024*1024)
	if err != nil {
		log.SetOutput(os.Stderr)
		return
	}
	logFile = f
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func logSync() {
	if logFile != nil {
		_ = logFile.Close()
	}
}
