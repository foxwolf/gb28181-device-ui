//go:build !windows

package ui

func tryPlaceOnCursorMonitor(ev any, hwnd *uintptr, placed *bool) {}
