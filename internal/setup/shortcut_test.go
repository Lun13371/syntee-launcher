package setup

import (
	"testing"
	"time"
)

// A path containing an apostrophe (e.g. a user profile like "C:\Users\O'Brien")
// used to break out of the single-quoted PowerShell literal, which made
// createShortcut fail (or run attacker-chosen commands). Escaping must double
// the quote so the path stays one literal.
func TestPSQuoteEscapesSingleQuotes(t *testing.T) {
	cases := map[string]string{
		`C:\Users\Luneex\Desktop\Syntee Helper.lnk`: `C:\Users\Luneex\Desktop\Syntee Helper.lnk`,
		`C:\Users\O'Brien\Desktop\a.lnk`:            `C:\Users\O''Brien\Desktop\a.lnk`,
		`'; Start-Process calc; '`:                  `''; Start-Process calc; ''`,
	}
	for in, want := range cases {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// The shortcut creation must be bounded: a hung powershell.exe (AV script
// scanning, AppLocker, broken profile) used to block the launcher forever on
// the "Проверка обновлений…" screen before the update check even started.
func TestShortcutTimeoutIsBounded(t *testing.T) {
	if shortcutTimeout <= 0 {
		t.Fatalf("shortcutTimeout must be positive, got %v", shortcutTimeout)
	}
	if shortcutTimeout > 30*time.Second {
		t.Fatalf("shortcutTimeout too long: %v", shortcutTimeout)
	}
}
