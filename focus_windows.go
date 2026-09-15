//go:build windows

package main

import (
	"sync/atomic"
	"syscall"
)

// lastRealForeground tracks the most recent foreground window that isn't
// the desktop shell itself, updated passively via a WinEventHook.
//
// Reading GetForegroundWindow() directly at the moment of a Win+<digit>
// keypress is unreliable: diagnostic logging showed holding Win can shift
// the foreground window to Explorer's desktop ("Progman"/"WorkerW")
// essentially synchronously -- sometimes before even the earliest point
// this app can query it (Win's own key-down, tried first and still not
// reliable enough). Passive tracking sidesteps the race entirely: it
// simply never records those transient shell-focus events in the first
// place, so whatever it holds is always the last genuine app window,
// regardless of how narrow the window to query it would have been.
var lastRealForeground atomic.Uintptr

const (
	eventSystemForeground = 3
	winEventOutOfContext  = 0
)

// startForegroundTracker installs the WinEventHook and returns a func
// that uninstalls it. Must be called from a thread that pumps a Windows
// message loop: WINEVENT_OUTOFCONTEXT events are delivered via the
// calling thread's queue, same as the keyboard hook already requires.
func startForegroundTracker() (uninstall func()) {
	callback := syscall.NewCallback(onForegroundChanged)
	h, _, _ := procSetWinEventHook.Call(
		uintptr(eventSystemForeground), uintptr(eventSystemForeground),
		0, callback, 0, 0, uintptr(winEventOutOfContext),
	)
	return func() {
		if h != 0 {
			procUnhookWinEvent.Call(h)
		}
	}
}

// onForegroundChanged is the WinEventProc callback (WINEVENTPROC):
// func(hWinEventHook, event, hwnd, idObject, idChild, idEventThread, dwmsEventTime uintptr) uintptr
func onForegroundChanged(_, _, hwnd, _, _, _, _ uintptr) uintptr {
	if hwnd != 0 && !isShellDesktopWindow(hwnd) {
		lastRealForeground.Store(hwnd)
	}
	return 0
}

func isShellDesktopWindow(hwnd uintptr) bool {
	switch windowClassName(hwnd) {
	case "Progman", "WorkerW":
		return true
	default:
		return false
	}
}
