//go:build !windows

package ui

import "image"

func clientCursor(hwnd uintptr) (image.Point, bool) {
	return image.Point{}, false
}

func windowClientSize(hwnd uintptr) (image.Point, bool) {
	return image.Point{}, false
}
