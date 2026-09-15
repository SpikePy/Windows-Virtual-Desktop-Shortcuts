//go:build windows

// Command VirtualDesktopShortcuts lets you jump directly to the Nth Windows
// virtual desktop with Win+1 through Win+9, the way Win+1..9 already jumps
// to the Nth pinned taskbar app, and move the focused window to the Nth
// virtual desktop with Win+Shift+1 through Win+Shift+9.
//
// It runs quietly in the system tray. Right-click the tray icon to Enable
// or Disable the shortcuts, Configure the app via its YAML config file, or
// Exit.
package main

import (
	"errors"
	"runtime"

	"golang.org/x/sys/windows"
)

const singleInstanceMutexName = `Local\VDSwitcher-SingleInstance-Mutex`

// version is overridden at build time via -ldflags "-X main.version=v1.2.3"
// (the release workflow sets it to the pushed tag). Shown in the tray icon
// tooltip.
var version = "dev"

func main() {
	mutexNamePtr := mustUTF16Ptr(singleInstanceMutexName)
	_, err := windows.CreateMutex(nil, false, mutexNamePtr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		messageBoxError(appName+" is already running (check the system tray).", appName)
		return
	}

	startConfigWatcher()

	requests := make(chan desktopRequest, 2)
	go runDesktopSwitcher(requests)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := runApp(requests); err != nil {
		messageBoxError(err.Error(), appName)
	}
	close(requests)
}
