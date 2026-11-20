//go:build windows

package ui

import (
	"time"
	"unsafe"

	"gioui.org/app"
	"golang.org/x/sys/windows"
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procScreenToClient  = user32.NewProc("ScreenToClient")
	procGetClientRect   = user32.NewProc("GetClientRect")
	procMonitorFromRect = user32.NewProc("MonitorFromRect")
	procGetMonitorInfoW = user32.NewProc("GetMonitorInfoW")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procSetWindowPos    = user32.NewProc("SetWindowPos")
)

const (
	monitorDefaultToNearest = 2
	swpNoZOrder             = 0x0004
	swpNoActivate           = 0x0010
	swpAsyncWindowPos       = 0x4000
)

type winPoint struct{ X, Y int32 }

type winRect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	CbSize  uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

// tryPlaceOnCursorMonitor 在拿到 HWND 后，到后台去居中，避免在 Gio 事件循环里
// 同步调用 SetWindowPos 导致窗口假死。
func tryPlaceOnCursorMonitor(ev any, hwnd *uintptr, placed *bool) {
	ve, ok := ev.(app.Win32ViewEvent)
	if !ok {
		return
	}
	if !ve.Valid() {
		*hwnd = 0
		return
	}
	*hwnd = ve.HWND
	if *placed {
		return
	}
	*placed = true
	go delayedCenterOnCursorMonitor(ve.HWND)
}

func delayedCenterOnCursorMonitor(hwnd uintptr) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if windowHasSize(hwnd) {
			centerOnCursorMonitor(hwnd)
			return
		}
		if time.Now().After(deadline) {
			centerOnCursorMonitor(hwnd)
			return
		}
		time.Sleep(16 * time.Millisecond)
	}
}

func windowHasSize(hwnd uintptr) bool {
	var wr winRect
	ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	return ok != 0 && wr.Right-wr.Left >= 200 && wr.Bottom-wr.Top >= 200
}

func centerOnCursorMonitor(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	var pt winPoint
	ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if ok == 0 {
		return
	}
	spot := winRect{Left: pt.X, Top: pt.Y, Right: pt.X + 1, Bottom: pt.Y + 1}
	hmon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&spot)), monitorDefaultToNearest)
	if hmon == 0 {
		return
	}
	var mi monitorInfo
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	ok, _, _ = procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
	if ok == 0 {
		return
	}
	var wr winRect
	ok, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	if ok == 0 {
		return
	}
	dx := wr.Right - wr.Left
	dy := wr.Bottom - wr.Top
	if dx <= 0 || dy <= 0 {
		return
	}
	area := mi.Work
	const margin int32 = 8
	maxW := area.Right - area.Left - margin*2
	maxH := area.Bottom - area.Top - margin*2
	if maxW < 200 {
		maxW = area.Right - area.Left
	}
	if maxH < 200 {
		maxH = area.Bottom - area.Top
	}
	if dx > maxW {
		dx = maxW
	}
	if dy > maxH {
		dy = maxH
	}
	x := (area.Right + area.Left - dx) / 2
	y := (area.Bottom + area.Top - dy) / 2
	if x < area.Left+margin {
		x = area.Left + margin
	}
	if y < area.Top+margin {
		y = area.Top + margin
	}
	if x+dx > area.Right-margin {
		x = area.Right - margin - dx
	}
	if y+dy > area.Bottom-margin {
		y = area.Bottom - margin - dy
	}
	procSetWindowPos.Call(
		hwnd,
		0,
		uintptr(x),
		uintptr(y),
		uintptr(dx),
		uintptr(dy),
		swpNoZOrder|swpNoActivate|swpAsyncWindowPos,
	)
}
