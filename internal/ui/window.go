package ui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Window activation is used for the "launcher already running" path: instead of
// telling the user that an installer is running (which used to happen while the
// first window merely waited for a click), the existing window is brought to
// the foreground.
var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procFindWindow      = user32.NewProc("FindWindowW")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
)

const (
	swShowNormal = 1
	swRestore    = 9
)

// ActivateWindow restores and focuses a top-level window by exact title.
// Returns false when no such window exists (another process may own the
// single-instance mutex without having created its window yet).
func ActivateWindow(title string) bool {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return false
	}
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		procShowWindow.Call(hwnd, swShowNormal)
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForeground.Call(hwnd)
	return true
}
