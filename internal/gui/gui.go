// Package gui provides a glassmorphism WebView2-based UI for the launcher.
// WebView2 is pre-installed on Windows 10/11 (ships with Edge); the library
// finds it automatically. For Linux/macOS, use go-webview-selector.
package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jchv/go-webview2"
)

type State string

const (
	StateWelcome         State = "welcome"
	StateChecking        State = "checking"
	StateDownloading     State = "downloading"
	StateExtracting      State = "extracting"
	StateUpdateAvailable State = "update_available"
	StateForcedUpdate    State = "forced_update"
	StateInstalled       State = "installed"
	StateDone            State = "done"
	StateLaunching       State = "launching"
	StateError           State = "error"
)

// InstallOptions carries the user's choices from the welcome screen.
type InstallOptions struct {
	DesktopShortcut bool `json:"desktopShortcut"`
	StartMenuShortcut bool `json:"startMenuShortcut"`
	Autostart        bool `json:"autostart"`
}

type Progress struct {
	Percent      int     `json:"percent"`
	DownloadedMB float64 `json:"downloadedMB"`
	TotalMB      float64 `json:"totalMB"`
	SpeedMBps    float64 `json:"speedMBps"`
	EtaSec       int     `json:"etaSec"`
}

type Payload struct {
	State        State     `json:"state"`
	Title        string    `json:"title,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Progress     *Progress `json:"progress,omitempty"`
	Version      string    `json:"version,omitempty"`
	Changelog    string    `json:"changelog,omitempty"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	Force        bool      `json:"force,omitempty"`
	IsFirstRun   bool      `json:"isFirstRun,omitempty"`
}

// VerifyConfig is implemented by config.Config so gui can check file integrity
// without importing the config package (avoids circular dependency).
type VerifyConfig interface {
	Dir() string
	CurrentDir() string
	ClientExePath() string
}

type GUI struct {
	wv       webview2.WebView
	hwnd     uintptr
	htmlPath string

	mu       sync.Mutex
	state    Payload
	lastPush time.Time
	speed    speedTracker

	clientRunning atomic.Bool
	decisionCh    chan bool
	retryCh       chan struct{}
	installCh     chan InstallOptions
	reinstallCh   chan struct{}
	launchCh      chan struct{}
	verifyCfg     VerifyConfig
}

func New() (*GUI, error) {
	const (
		winW = 640
		winH = 520
	)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     true,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Syntee Helper",
			Width:  winW,
			Height: winH,
			Center: true,
		},
	})
	if w == nil {
		return nil, fmt.Errorf("webview2 init failed (is WebView2 runtime installed?)")
	}

	hwnd := uintptr(w.Window())

	// Frameless: remove Windows titlebar, then resize so the webview
	// fills the entire borderless window.
	makeFrameless(hwnd, winW, winH)

	g := &GUI{
		wv:          w,
		hwnd:        hwnd,
		decisionCh:  make(chan bool, 1),
		retryCh:     make(chan struct{}, 1),
		installCh:   make(chan InstallOptions, 1),
		reinstallCh: make(chan struct{}, 1),
		launchCh:    make(chan struct{}, 1),
	}

	g.bindFunctions()

	// Write HTML to a temp file and navigate via file:// URL.
	tmpFile, err := os.CreateTemp("", "syntee-launcher-*.html")
	if err != nil {
		return nil, fmt.Errorf("create temp html: %w", err)
	}
	if _, err := tmpFile.Write([]byte(pageHTML())); err != nil {
		return nil, fmt.Errorf("write temp html: %w", err)
	}
	tmpFile.Close()
	g.htmlPath = tmpFile.Name()
	w.Navigate("file:///" + strings.ReplaceAll(tmpFile.Name(), "\\", "/"))

	return g, nil
}

func (g *GUI) Run(lifecycle func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("lifecycle panic: %v", r)
			}
			g.wv.Dispatch(func() { g.wv.Terminate() })
		}()
		lifecycle()
	}()
	g.wv.Run()
	// Clean up the temp HTML file after the webview loop exits.
	if g.htmlPath != "" {
		_ = os.Remove(g.htmlPath)
	}
}

func (g *GUI) SetState(state State, payload *Payload) {
	if payload == nil {
		payload = &Payload{}
	}
	payload.State = state
	g.mu.Lock()
	g.state = *payload
	g.mu.Unlock()
	// Always show the window for interactive states — the user may have
	// hidden it earlier (e.g. pressed Close during download). Without this,
	// an error or launching screen would be invisible and the launcher
	// would appear to hang.
	switch state {
	case StateError, StateLaunching, StateUpdateAvailable, StateInstalled, StateWelcome, StateDone:
		g.Show()
	}
	g.push(payload)
}

func (g *GUI) SetProgress(done, total int64) {
	g.mu.Lock()
	now := time.Now()
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	if pct < 100 && now.Sub(g.lastPush) < 50*time.Millisecond {
		g.mu.Unlock()
		return
	}
	g.lastPush = now
	speed := g.speed.update(done)
	pr := &Progress{
		Percent:      pct,
		DownloadedMB: float64(done) / 1024 / 1024,
		TotalMB:      float64(total) / 1024 / 1024,
		SpeedMBps:    speed,
	}
	if speed > 0 && total > done {
		pr.EtaSec = int(float64(total-done)/1024/1024 / speed)
	}
	g.state.Progress = pr
	payload := g.state
	g.mu.Unlock()
	g.push(&payload)
}

func (g *GUI) ResetSpeed() {
	g.mu.Lock()
	g.speed.reset()
	g.mu.Unlock()
}

func (g *GUI) SetClientRunning(v bool) { g.clientRunning.Store(v) }
func (g *GUI) DecisionCh() chan bool          { return g.decisionCh }
func (g *GUI) RetryCh() chan struct{}         { return g.retryCh }
func (g *GUI) InstallCh() chan InstallOptions { return g.installCh }
func (g *GUI) ReinstallCh() chan struct{}     { return g.reinstallCh }
func (g *GUI) SetVerifyConfig(cfg VerifyConfig) { g.verifyCfg = cfg }
func (g *GUI) LaunchCh() chan struct{}          { return g.launchCh }

func (g *GUI) Show() { g.wv.Dispatch(func() { showHwnd(g.hwnd, swShow) }) }
func (g *GUI) Hide() { g.wv.Dispatch(func() { showHwnd(g.hwnd, swHide) }) }

func (g *GUI) SetCountdown(sec int) {
	js := fmt.Sprintf("window.__setCountdown(%d)", sec)
	g.wv.Dispatch(func() { g.wv.Eval(js) })
}

func (g *GUI) Terminate() {
	g.wv.Dispatch(func() { g.wv.Terminate() })
}

func (g *GUI) GetCurrentState() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state.State
}

func (g *GUI) push(p *Payload) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	js := fmt.Sprintf("window.__updateState(%s)", string(data))
	g.wv.Dispatch(func() { g.wv.Eval(js) })
}

func (g *GUI) bindFunctions() {
	bind := func(name string, fn interface{}) {
		if err := g.wv.Bind(name, fn); err != nil {
			log.Printf("bind %s: %v", name, err)
		}
	}
	bind("updateNow", func() {
		select { case g.decisionCh <- true: default: }
	})
	bind("later", func() {
		select { case g.decisionCh <- false: default: }
	})
	bind("retry", func() {
		select { case g.retryCh <- struct{}{}: default: }
	})
	bind("startInstall", func(opts InstallOptions) {
		select { case g.installCh <- opts: default: }
	})
	bind("reinstall", func() {
		select { case g.reinstallCh <- struct{}{}: default: }
	})
	bind("launchClient", func() {
		select { case g.launchCh <- struct{}{}: default: }
	})
	bind("verifyIntegrity", func() map[string]interface{} {
		return g.verifyIntegrity()
	})
	bind("closeWindow", func() {
		switch g.GetCurrentState() {
		case StateUpdateAvailable:
			select { case g.decisionCh <- false: default: }
			g.wv.Terminate()
		default:
			// For all other states (including downloading, extracting,
			// launching, error, installed, welcome, done) — terminate.
			// Hiding instead would leave the process alive with no UI,
			// making it impossible for the user to interact.
			g.wv.Terminate()
		}
	})
	bind("minimize", func() { g.Hide() })
	bind("startDrag", func() { startDrag(g.hwnd) })
}

// ── speed tracker ──

type speedTracker struct {
	samples   []speedSample
	lastBytes int64
	lastTime  time.Time
}

type speedSample struct {
	bytes int64
	at    time.Time
}

func (s *speedTracker) update(downloaded int64) float64 {
	now := time.Now()
	if s.lastTime.IsZero() {
		s.lastTime = now
		s.lastBytes = downloaded
		return 0
	}
	delta := downloaded - s.lastBytes
	s.samples = append(s.samples, speedSample{bytes: delta, at: now})
	s.lastBytes = downloaded
	s.lastTime = now
	if len(s.samples) > 15 {
		s.samples = s.samples[len(s.samples)-15:]
	}
	if len(s.samples) < 2 {
		return 0
	}
	var total int64
	for _, smp := range s.samples {
		total += smp.bytes
	}
	dur := s.samples[len(s.samples)-1].at.Sub(s.samples[0].at)
	if dur <= 0 {
		return 0
	}
	return float64(total) / dur.Seconds() / 1024 / 1024
}

func (s *speedTracker) reset() {
	s.samples = nil
	s.lastBytes = 0
	s.lastTime = time.Time{}
}

// Suppress unused import (strings used in assets.go, referenced here for linker)
var _ = strings.Replace
