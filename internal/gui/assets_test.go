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
	if len(faviconICO) < 6 || string(faviconICO[:4]) != "\x00\x00\x01\x00" {
		t.Fatal("invalid ICO header")
	}
}
