package ui

import (
	"image"
	"unsafe"
)

func clientCursor(hwnd uintptr) (image.Point, bool) {
	if hwnd == 0 {
		return image.Point{}, false
	}
	var p winPoint
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return image.Point{}, false
	}
	r, _, _ = procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return image.Point{}, false
	}
	return image.Pt(int(p.X), int(p.Y)), true
}

func windowClientSize(hwnd uintptr) (image.Point, bool) {
	if hwnd == 0 {
		return image.Point{}, false
	}
	var r winRect
	ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return image.Point{}, false
	}
	return image.Pt(int(r.Right-r.Left), int(r.Bottom-r.Top)), true
}
