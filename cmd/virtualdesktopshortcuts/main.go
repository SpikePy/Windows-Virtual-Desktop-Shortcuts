//go:build windows

// Command virtualdesktopshortcuts runs in the background and turns
// Ctrl+Alt+1..9 into "switch to virtual desktop N", Ctrl+Alt+Left/Right
// and Ctrl+Alt+wheel up/down into "switch to the previous/next desktop",
// and the same with Shift into moving the focused window there instead.
//
// Only the left Alt key counts: on layouts where the right Alt key is
// AltGr it reports Ctrl+Alt as held too, and AltGr+digit types characters
// such as '{' that have to keep working.
//
// A tray icon (four tiles = shortcuts active, the same tiles greyed out
// with a diagonal red strike = turned off) lets the user switch them off
// without stopping the process: left-click toggles, right-click opens an
// Enable/Disable/Configure/Exit menu. Configure opens config.yaml in
// whatever application Windows has associated with .yaml files.
//
// This program does not exit on its own. To stop it: the tray menu's Exit,
// Task Manager, taskkill, or the Setup program (which does it
// automatically when updating).
//
// Logging is OFF by default. Pass -enable-logging to write diagnostics to
// VirtualDesktopShortcuts.log next to the exe, for troubleshooting only.
//
// Built with -ldflags "-H=windowsgui" so it never shows a console window;
// everything below runs inside a top-level recover() that never lets a
// panic surface as a crash dialog - diagnostics go only to the log file.
package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/applog"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/config"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/hook"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/hotkeys"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/shortcut"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/singleinstance"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/tray"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/vdesktop"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/win32"
)

// version is stamped in at build time via -ldflags "-X main.version=...";
// left as "dev" for local/manual builds.
var version = "dev"

const (
	appName     = "Virtual Desktop Shortcuts"
	mutexName   = `Local\VirtualDesktopShortcuts_SingleInstance`
	logFileName = "VirtualDesktopShortcuts.log"

	// exeName is the installed exe, next to config.yaml - what the Startup
	// shortcut points at.
	exeName = "VirtualDesktopShortcuts.exe"

	// configPollInterval is how often the config file is checked for
	// outside edits, e.g. made in the editor Configure opened.
	configPollInterval = 2 * time.Second
)

// Tray menu item IDs. 0 is reserved for "nothing selected".
const (
	menuEnable = iota + 1
	menuDisable
	menuConfigure
	menuExit
)

func main() {
	// Win32 hooks, windows and the message queue are bound to the OS
	// thread that creates them; the Go runtime must never migrate this
	// goroutine to a different one mid-run.
	runtime.LockOSThread()

	cfg, cfgErr := config.Load()

	enabled := flag.Bool("enabled", cfg.Enabled, "whether the shortcuts are active on launch (overrides config.yaml)")
	enableLogging := flag.Bool("enable-logging", false, "write diagnostics to "+logFileName+" next to the exe")
	flag.Parse()

	logf, logPath := applog.New(logFileName, *enableLogging)
	if cfgErr != nil {
		logf("WARNING loading config.yaml (falling back to enabled=%t): %v", config.DefaultEnabled, cfgErr)
	}

	release, alreadyRunning, err := singleinstance.Acquire(mutexName)
	if err != nil {
		logf("EXCEPTION acquiring single-instance mutex: %v", err)
		return
	}
	if alreadyRunning {
		logf("Another instance is already running - exiting.")
		win32.ErrorBox(appName, appName+" is already running (check the system tray).")
		return
	}
	defer release()

	defer func() {
		if r := recover(); r != nil {
			logf("PANIC: %v", r)
		}
	}()

	a := &app{logf: logf}
	a.enabled.Store(*enabled)
	a.applyAutostart(cfg.Autostart)

	if err := a.run(); err != nil {
		logf("EXCEPTION %v", err)
		win32.ErrorBox(appName, err.Error())
		return
	}
	logf("Exited cleanly. Log at: %s", logPath)
}

// app owns the tray icon and the shared enabled state. Every method except
// the config watcher's callback runs on the single OS thread that pumps
// the message loop.
type app struct {
	logf func(format string, args ...any)
	hwnd uintptr

	// enabled is read by the keyboard hook on every keystroke and written
	// by the tray, the menu and the config watcher, so it's atomic.
	enabled atomic.Bool

	iconEnabled  uintptr
	iconDisabled uintptr
}

func (a *app) run() error {
	requests := make(chan hotkeys.Request, 2)
	defer close(requests)
	go vdesktop.Run(requests, a.logf)

	var err error
	if a.iconEnabled, err = tray.EnabledIcon(); err != nil {
		return fmt.Errorf("building the tray icon: %w", err)
	}
	defer tray.DestroyIconHandle(a.iconEnabled)
	if a.iconDisabled, err = tray.DisabledIcon(); err != nil {
		return fmt.Errorf("building the disabled tray icon: %w", err)
	}
	defer tray.DestroyIconHandle(a.iconDisabled)

	if a.hwnd, err = tray.NewWindow(appName, a.toggleEnabled, a.showMenu, a.refreshIcon); err != nil {
		return fmt.Errorf("creating the tray window: %w", err)
	}
	defer tray.DestroyWindow(a.hwnd)

	if err := a.showIcon(); err != nil {
		return fmt.Errorf("adding the tray icon: %w", err)
	}
	defer tray.RemoveIcon(a.hwnd)

	h := hook.New(requests, a.enabled.Load)
	if err := h.Install(); err != nil {
		return fmt.Errorf("installing the input hooks: %w", err)
	}
	defer h.Uninstall()

	stopWatching := make(chan struct{})
	defer close(stopWatching)
	go config.Watch(stopWatching, configPollInterval, a.configChanged)

	a.logf("Running %s (enabled=%t)", version, a.enabled.Load())
	win32.RunMessageLoop()
	return nil
}

// showIcon puts the icon matching the current state in the tray, with the
// app name, version and enabled/disabled state in its tooltip.
func (a *app) showIcon() error {
	icon, state := a.iconEnabled, "enabled"
	if !a.enabled.Load() {
		icon, state = a.iconDisabled, "disabled"
	}
	return tray.SetIcon(a.hwnd, icon, fmt.Sprintf("%s %s (%s)", appName, version, state))
}

// refreshIcon updates the tray icon, called on the message-loop thread
// after the state changed anywhere.
func (a *app) refreshIcon() {
	if err := a.showIcon(); err != nil {
		a.logf("WARNING updating the tray icon: %v", err)
	}
}

// applyAutostart adds or removes the Startup shortcut to the installed exe
// to match the autostart setting. A failure is only logged: the shortcuts
// work either way.
func (a *app) applyAutostart(on bool) {
	path, err := config.Path()
	if err == nil {
		err = shortcut.Autostart(on, filepath.Join(filepath.Dir(path), exeName))
	}
	if err != nil {
		a.logf("WARNING updating autostart=%t: %v", on, err)
	}
}

// configChanged runs on the watcher's goroutine, so it only stores the new
// state and asks the message-loop thread to redraw the icon.
func (a *app) configChanged(cfg config.Config) {
	a.applyAutostart(cfg.Autostart)
	if cfg.Enabled == a.enabled.Load() {
		return
	}
	a.enabled.Store(cfg.Enabled)
	a.logf("config.yaml changed: enabled=%t", cfg.Enabled)
	win32.PostMessage(a.hwnd, win32.WMConfigChanged, 0, 0)
}

func (a *app) toggleEnabled() { a.setEnabled(!a.enabled.Load()) }

// setEnabled updates the state the hook reads, writes it back to
// config.yaml so the tray and the file never disagree, and refreshes the
// icon.
func (a *app) setEnabled(enabled bool) {
	a.enabled.Store(enabled)
	if err := config.SetEnabled(enabled); err != nil {
		a.logf("WARNING saving enabled=%t to config.yaml: %v", enabled, err)
		win32.ErrorBox(appName, fmt.Sprintf("The shortcuts are now %s, but saving that to config.yaml failed:\n%v",
			map[bool]string{true: "on", false: "off"}[enabled], err))
	}
	a.refreshIcon()
}

func (a *app) showMenu() {
	enabled := a.enabled.Load()
	switch tray.ShowMenu(a.hwnd, []tray.MenuItem{
		{ID: menuEnable, Label: "Enable", Checked: enabled},
		{ID: menuDisable, Label: "Disable", Checked: !enabled},
		{},
		{ID: menuConfigure, Label: "Configure"},
		{},
		{ID: menuExit, Label: "Exit"},
	}) {
	case menuEnable:
		a.setEnabled(true)
	case menuDisable:
		a.setEnabled(false)
	case menuConfigure:
		a.configure()
	case menuExit:
		win32.Quit()
	}
}

// configure makes sure config.yaml exists, then opens it for editing.
func (a *app) configure() {
	path, err := config.Ensure()
	if err != nil {
		a.logf("WARNING creating config.yaml: %v", err)
		win32.ErrorBox(appName, fmt.Sprintf("Couldn't create the config file:\n%v", err))
		return
	}
	if err := win32.OpenWithDefaultApp(a.hwnd, path); err != nil {
		a.logf("WARNING opening %s: %v", path, err)
		win32.ErrorBox(appName, fmt.Sprintf("Couldn't open an editor for:\n%s", path))
	}
}
