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
	"syscall"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Lun13371/syntee-launcher/internal/gui"
	"github.com/Lun13371/syntee-launcher/internal/rollback"
	"github.com/Lun13371/syntee-launcher/internal/setup"
	"github.com/Lun13371/syntee-launcher/internal/ui"
	"github.com/Lun13371/syntee-launcher/internal/updater"
)

func jsonMarshal(v any) ([]byte, error)    { return json.MarshalIndent(v, "", "  ") }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func main() {
	initLogging()
	defer logSync()

	log.Printf("syntee-launcher starting (pid=%d, args=%v)", os.Getpid(), os.Args[1:])

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

	g.Run(func() {
		runLifecycle(ctx, cfg, client, g)
	})
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
		installOpts := <-g.InstallCh()
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
				<-g.RetryCh()
				g.ResetSpeed()
				g.SetState(gui.StateDownloading, &gui.Payload{IsFirstRun: true})
				continue
			}
			break
		}

		// Installation complete — launch client with countdown.
		launchClientAndCountdown(cfg, g)
		return
	}

	// ── 2. Already installed: check for updates ─────────────────────────
	// Ensure shortcuts exist (they might be missing if a previous install
	// was done before the shortcut fix, or if the user deleted them).
	if err := setup.EnsureShortcuts(cfg); err != nil {
		log.Printf("ensure shortcuts: %v", err)
	}

	g.SetState(gui.StateChecking, nil)

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

	if err == nil && manifest != nil && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
		log.Printf("update available: %s -> %s (force=%v)", cfg.CurrentVersion, manifest.Version, manifest.Force)
		if manifest.Force {
			g.SetState(gui.StateForcedUpdate, &gui.Payload{
				Version: manifest.Version,
				Force:   true,
			})
			g.ResetSpeed()
			if err := doUpdate(ctx, cfg, client, manifest, g); err != nil {
				log.Printf("forced update failed: %v", err)
				g.SetState(gui.StateError, &gui.Payload{
					ErrorMessage: "Ошибка обновления: " + err.Error(),
				})
				<-g.RetryCh()
				os.Exit(1)
			}
		} else {
			g.SetState(gui.StateUpdateAvailable, &gui.Payload{
				Version:   manifest.Version,
				Changelog: manifest.Changelog,
			})
			if <-g.DecisionCh() {
				g.SetState(gui.StateDownloading, &gui.Payload{
					Version: manifest.Version,
				})
				g.ResetSpeed()
				if err := doUpdate(ctx, cfg, client, manifest, g); err != nil {
					log.Printf("update failed: %v", err)
					g.SetState(gui.StateError, &gui.Payload{
						ErrorMessage: "Ошибка обновления: " + err.Error(),
					})
					<-g.RetryCh()
					os.Exit(1)
				}
			} else {
				// User declined update — show installed screen.
				showInstalledScreen(ctx, cfg, client, g)
				return
			}
		}

		// Update complete — launch client with countdown.
		launchClientAndCountdown(cfg, g)
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
			launchClientAndCountdown(cfg, g)
			return
		case <-g.ReinstallCh():
			log.Printf("reinstall requested")
			g.SetState(gui.StateDownloading, &gui.Payload{
				Title:      "Переустановка Syntee Helper",
				IsFirstRun: true,
			})
			g.ResetSpeed()

			// Fetch latest manifest (use "0.0.0" to always get a result).
			manifest, err := client.Check(ctx, cfg.Channel, "0.0.0")
			if err != nil {
				log.Printf("reinstall: fetch manifest: %v", err)
				g.SetState(gui.StateError, &gui.Payload{
					ErrorMessage: "Не удалось получить информацию о версии.\n\n" + err.Error(),
				})
				<-g.RetryCh()
				g.SetState(gui.StateInstalled, &gui.Payload{
					Title:   cfg.Dir(),
					Version: cfg.CurrentVersion,
				})
				continue
			}
			if manifest == nil {
				g.SetState(gui.StateError, &gui.Payload{
					ErrorMessage: "Нет доступной версии для установки.",
				})
				<-g.RetryCh()
				g.SetState(gui.StateInstalled, &gui.Payload{
					Title:   cfg.Dir(),
					Version: cfg.CurrentVersion,
				})
				continue
			}

		if err := doUpdate(ctx, cfg, client, manifest, g); err != nil {
			log.Printf("reinstall failed: %v", err)
			g.SetState(gui.StateError, &gui.Payload{
				ErrorMessage: "Ошибка переустановки: " + err.Error(),
			})
			<-g.RetryCh()
			g.SetState(gui.StateInstalled, &gui.Payload{
				Title:   cfg.Dir(),
				Version: cfg.CurrentVersion,
			})
			continue
		}

		// Recreate shortcuts pointing to client (not launcher).
		if err := setup.RecreateShortcuts(cfg); err != nil {
			log.Printf("reinstall: recreate shortcuts: %v", err)
		}

		// Reinstall complete — launch client with countdown.
		launchClientAndCountdown(cfg, g)
		return
		}
	}
}

// launchClientDetached starts the client process detached so it survives
// launcher exit. Used by the headless fallback (no GUI countdown).
func launchClientDetached(cfg *config.Config) {
	exe := cfg.ClientExePath()
	if _, err := os.Stat(exe); err != nil {
		log.Printf("headless launch: client exe not found: %v", err)
		return
	}
	cmd := exec.Command(exe)
	cmd.Dir = cfg.CurrentDir()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		log.Printf("headless launch: failed to start client: %v", err)
		return
	}
	log.Printf("headless: launched client (pid=%d)", cmd.Process.Pid)
	_ = cmd.Process.Release()
}

// launchClientAndCountdown launches the client detached, shows a countdown
// screen, and terminates the launcher after 10 seconds.
func launchClientAndCountdown(cfg *config.Config, g *gui.GUI) {
	exe := cfg.ClientExePath()
	if _, err := os.Stat(exe); err != nil {
		log.Printf("launch: client exe not found: %v", err)
		g.SetState(gui.StateError, &gui.Payload{
			ErrorMessage: "Клиент не найден. Переустановите приложение.",
		})
		<-g.RetryCh()
		return
	}

	// Launch client detached so it survives launcher exit.
	cmd := exec.Command(exe)
	cmd.Dir = cfg.CurrentDir()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS so the client is independent.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		log.Printf("launch: failed to start client: %v", err)
		g.SetState(gui.StateError, &gui.Payload{
			ErrorMessage: "Не удалось запустить клиент: " + err.Error(),
		})
		<-g.RetryCh()
		return
	}
	log.Printf("launched client (pid=%d)", cmd.Process.Pid)
	// Release the process so it doesn't become a zombie when we exit.
	_ = cmd.Process.Release()

	// Show countdown screen.
	g.SetState(gui.StateLaunching, nil)

	// Countdown 10 seconds, then terminate.
	for i := 10; i > 0; i-- {
		g.SetCountdown(i)
		time.Sleep(1 * time.Second)
	}

	log.Printf("countdown finished, exiting launcher")
	g.Terminate()
}

// killClientIfRunning kills any running instance of the client so the updater
// can rename/replace files in current/. Uses taskkill /F with CREATE_NO_WINDOW
// so no console pops up. If the client isn't running, taskkill exits non-zero
// — we ignore the error. After killing, waits 2s for the OS to release handles.
func killClientIfRunning(cfg *config.Config) {
	exeName := cfg.ClientExe
	cmd := exec.Command("taskkill", "/IM", exeName, "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	if err := cmd.Run(); err != nil {
		log.Printf("killClient: taskkill %s: %v (ok if not running)", exeName, err)
		return
	}
	log.Printf("killClient: killed %s, waiting for handles to release", exeName)
	time.Sleep(2 * time.Second)
}

// doUpdate downloads and applies an update, reporting progress to the GUI.
// Used for both forced and user-accepted updates.
func doUpdate(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest, g *gui.GUI) error {
	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), func(done, total int64) {
		g.SetProgress(done, total)
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("downloaded %s in %s (sha256 ok)", res.Path, res.Duration)

	g.SetState(gui.StateExtracting, nil)
	g.ResetSpeed()
	killClientIfRunning(cfg)
	if err := updater.Apply(res.Path, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion,
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
	data, err := os.ReadFile(exePath)
	if err != nil {
		log.Printf("copyUpdater: read: %v", err)
		return
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		log.Printf("copyUpdater: write: %v", err)
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
	} else if manifest != nil && updater.IsNewer(cfg.CurrentVersion, manifest.Version) {
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
	launchClientDetached(cfg)

	log.Printf("launcher done, exiting")
}

func doForcedUpdateHeadless(ctx context.Context, cfg *config.Config, client *updater.Client, m *updater.Manifest) error {
	ui.ShowForceProgress("Загрузка обновления " + m.Version + "…")

	res, err := client.DownloadToStaging(ctx, m, cfg.StagingDir(), func(done, total int64) {
		ui.ShowForceProgress("Загрузка обновления " + m.Version + "…\n" + ui.FormatPercent(done, total))
	})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("downloaded %s in %s (sha256 ok)", res.Path, res.Duration)

	killClientIfRunning(cfg)
	if err := updater.Apply(res.Path, cfg.CurrentDir(), cfg.BackupsDir(), cfg.StagingDir(), cfg.CurrentVersion, nil); err != nil {
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

var logFile *os.File

func initLogging() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	f, err := os.OpenFile(exe[:len(exe)-len(".exe")]+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
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
