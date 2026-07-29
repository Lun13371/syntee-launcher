package gui

import "syscall"

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	procGetWindowLong = user32.NewProc("GetWindowLongW")
	procSetWindowLong = user32.NewProc("SetWindowLongW")
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSendMessage   = user32.NewProc("SendMessageW")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
)

const (
	gwlStyle        uint32 = 0xFFFFFFF0
	wsPopup         uint32 = 0x80000000
	wsCaption       uint32 = 0x00C00000
	wsThickFrame    uint32 = 0x00040000
	wsSysMenu       uint32 = 0x00080000
	wsMinimizeBox   uint32 = 0x00020000
	wsMaximizeBox   uint32 = 0x00010000
	swpFrameChanged = 0x0020
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swHide          = 0
	swShow          = 5
	wmNclButtonDown = 0xA1
	htCaption       = 2
	wmSize          = 0x0005
)

// makeFrameless removes the Windows title bar and resize border, then
// forces a SetWindowPos with SWP_FRAME_CHANGED so the webview receives a
// WM_SIZE and re-layouts to fill the now-borderless client area.
func makeFrameless(hwnd uintptr, w, h int) {
	ret, _, _ := procGetWindowLong.Call(hwnd, uintptr(gwlStyle))
	style := uint32(ret)
	style &^= wsCaption | wsThickFrame | wsSysMenu | wsMinimizeBox | wsMaximizeBox
	style |= wsPopup
	procSetWindowLong.Call(hwnd, uintptr(gwlStyle), uintptr(style))
	// Resize to explicit w×h and trigger WM_SIZE → webview.Resize()
	procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(w), uintptr(h),
		swpFrameChanged|swpNoMove|swpNoZOrder|swpNoActivate)
}

func showHwnd(hwnd uintptr, cmd int) {
	procShowWindow.Call(hwnd, uintptr(cmd))
}

func startDrag(hwnd uintptr) {
	procReleaseCapture.Call()
	procSendMessage.Call(hwnd, wmNclButtonDown, htCaption, 0)
}
