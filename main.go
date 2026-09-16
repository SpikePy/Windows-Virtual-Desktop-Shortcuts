//go:build windows

// Command VirtualDesktopShortcuts lets you jump directly to the Nth Windows
// virtual desktop with Ctrl+Alt+1 through Ctrl+Alt+9, or to the previous or
// next one with Ctrl+Alt+Left/Right, and move the focused window there by
// adding Shift.
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
