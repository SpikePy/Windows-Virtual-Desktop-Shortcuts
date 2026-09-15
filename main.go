//go:build windows

// Command vdesktop-switcher lets you jump directly to the Nth Windows
// virtual desktop with Win+1 through Win+9, the way Win+1..9 already jumps
// to the Nth pinned taskbar app.
//
// It runs quietly in the system tray; right-click the tray icon and choose
// Exit to quit.
package main

import (
	"errors"
	"runtime"

	"golang.org/x/sys/windows"
)

const singleInstanceMutexName = `Local\VDSwitcher-SingleInstance-Mutex`

func main() {
	mutexNamePtr := mustUTF16Ptr(singleInstanceMutexName)
	_, err := windows.CreateMutex(nil, false, mutexNamePtr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		messageBoxError("Virtual Desktop Switcher is already running (check the system tray).", "Virtual Desktop Switcher")
		return
	}

	requests := make(chan int, 1)
	go runDesktopSwitcher(requests)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := runApp(requests); err != nil {
		messageBoxError(err.Error(), "Virtual Desktop Switcher")
	}
	close(requests)
}
