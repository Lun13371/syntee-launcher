package gui

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPageHTMLBrandAssets(t *testing.T) {
	html := pageHTML()
	logoURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(logoSVG)
	if got := strings.Count(html, `src="`+logoURI+`"`); got != 4 {
		t.Fatalf("expected ribbon S on all four brand screens, got %d", got)
	}
	faviconURI := "data:image/x-icon;base64," + base64.StdEncoding.EncodeToString(faviconICO)
	if !strings.Contains(html, `href="`+faviconURI+`"`) {
		t.Fatal("embedded favicon missing")
	}
	for _, stale := range []string{"{{LOGO}}", "{{FAVICON}}", "/*{{STYLE}}*/", "/*{{SCRIPT}}*/", "logoGrad", "<text "} {
		if strings.Contains(html, stale) {
			t.Errorf("unresolved placeholder or old brand: %s", stale)
		}
	}
	if !strings.Contains(string(logoSVG), `viewBox="620 60 300 460"`) {
		t.Fatal("unexpected ribbon S geometry")
	}
	if len(faviconICO) < 6 || string(faviconICO[0:4]) != "\x00\x00\x01\x00" {
		t.Fatal("invalid ICO header")
	}
}

// A screen that is visible before the host pushes a state must not claim a
// specific phase. The old default was a visible "Проверка обновлений…" spinner,
// so a first state that never reached the page (pushed while the document was
// still loading) was indistinguishable from a hung update check — on a first
// install it spun forever because the welcome screen never appeared.
func TestPageHTMLBootScreenIsStateDriven(t *testing.T) {
	html := pageHTML()
	if got := strings.Count(html, `<div class="screen" id="`); got != 1 {
		t.Fatalf("exactly one screen may be visible by default (the boot placeholder), got %d", got)
	}
	if !strings.Contains(html, `<div class="screen" id="screen-boot">`) {
		t.Fatal("boot placeholder screen missing")
	}
	for _, want := range []string{"window.ready()", "window.stateAck(seq||0)"} {
		if !strings.Contains(html, want) {
			t.Fatalf("app.js handshake call %q missing", want)
		}
	}
}
