// Package ui renders a minimal Windows message box for blocking force
// updates and fatal launcher errors. We deliberately avoid any GUI framework:
// a single MessageBox call is enough to tell the user "downloading required
// update…" without pulling in WinForms or a webview. The launcher is a
// console-less (-H windowsgui) binary, so printing to stdout would show
// nothing to the user.
//
// MessageBox blocks until the user clicks OK, so we run it in a goroutine for
// the progress case and let the launcher exit dismiss it implicitly (the
// process goes away, the box disappears). There is no graceful "close the
// box programmatically" path — that would require FindWindow + PostMessage
// from user32, which is overkill for a one-shot update dialog.
package ui

import (
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	mbOK              = 0x00000000
	mbIconInformation = 0x00000040
	mbIconError       = 0x00000010
	mbTopmost         = 0x00040000
	mbSetForeground   = 0x00010000
)

var progressShown sync.Once

// ShowForceProgress displays a non-blocking message box telling the user a
// required update is in progress. Idempotent — only one box is shown per
// process even if called repeatedly with updated text (we can't update the
// text in-place without a custom window, so subsequent calls are no-ops).
func ShowForceProgress(message string) {
	progressShown.Do(func() {
		title, _ := windows.UTF16PtrFromString("Syntee Helper — updating")
		text, _ := windows.UTF16PtrFromString(message)
		go windows.MessageBox(0, text, title, mbOK|mbIconInformation|mbTopmost|mbSetForeground)
	})
}

// DismissForceProgress is a no-op kept for API symmetry; the box is dismissed
// implicitly when the launcher process exits after applying the update.
func DismissForceProgress() {}

// ShowError pops a modal error box and blocks until dismissed. Use this for
// fatal launcher errors (e.g. "no backup to restore, please reinstall").
func ShowError(title, message string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(message)
	_, _ = windows.MessageBox(0, m, t, mbOK|mbIconError|mbTopmost|mbSetForeground)
}

// FormatPercent builds a human "12.3 MB / 150.0 MB (8%)" string.
func FormatPercent(done, total int64) string {
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	return fmt.Sprintf("%.1f MB / %.1f MB (%d%%)", float64(done)/1024/1024, float64(total)/1024/1024, pct)
}
