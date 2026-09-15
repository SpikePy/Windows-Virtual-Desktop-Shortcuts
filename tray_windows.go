//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayIconID = 1

	menuIDEnable    = 1001
	menuIDDisable   = 1002
	menuIDConfigure = 1003
	menuIDExit      = 1004

	// iconSyncTimerID drives a periodic check that keeps the tray icon's
	// graphic (enabled vs. disabled) in sync with hotkeysEnabled, which
	// can change from outside a direct tray click -- e.g. an external
	// edit to config.yaml, picked up by the background watcher in
	// config_windows.go.
	iconSyncTimerID    = 1
	iconSyncIntervalMs = 1000

	appName   = "Virtual Desktop Shortcuts"
	className = "VDSwitcherHiddenWindowClass"

	// Resource names of the two icon groups embedded via go-winres; see
	// winres/winres.json.
	iconResourceEnabled  = "APP"
	iconResourceDisabled = "APPDISABLED"
)

// trayApp owns the hidden window, tray icon and keyboard hook. Everything
// here must run on a single, dedicated OS thread: Win32 windows, hooks and
// message loops are all thread-affine.
type trayApp struct {
	hInstance windows.Handle
	hwnd      windows.Handle
	hook      *keyboardHook

	hIconEnabled  windows.Handle
	hIconDisabled windows.Handle

	// iconStateInit/lastEnabled let syncIconState skip redundant
	// Shell_NotifyIcon calls when nothing has actually changed.
	iconStateInit bool
	lastEnabled   bool
}

// runApp registers a hidden window, adds a tray icon, installs the
// low-level keyboard hook and pumps the Windows message loop until the
// user picks "Exit" from the tray menu or the window is otherwise
// destroyed. It must be called from a goroutine that has called
// runtime.LockOSThread and will not unlock it until this function returns.
func runApp(requests chan<- desktopRequest) error {
	hInstanceR, _, _ := procGetModuleHandleW.Call(0)
	app := &trayApp{hInstance: windows.Handle(hInstanceR)}

	app.hIconEnabled = loadNamedIcon(app.hInstance, iconResourceEnabled)
	app.hIconDisabled = loadNamedIcon(app.hInstance, iconResourceDisabled)
	cursorR, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))

	classNamePtr := mustUTF16Ptr(className)
	wndProcPtr := syscall.NewCallback(app.wndProc)

	var wc wndClassExW
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	wc.style = csHRedraw | csVRedraw
	wc.lpfnWndProc = wndProcPtr
	wc.hInstance = app.hInstance
	wc.hIcon = app.hIconEnabled
	wc.hIconSm = app.hIconEnabled
	wc.hCursor = windows.Handle(cursorR)
	wc.lpszClassName = classNamePtr

	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}

	hwndR, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(mustUTF16Ptr(appName))),
		0, // no WS_VISIBLE: the window is never shown
		0, 0, 0, 0,
		0, 0, uintptr(app.hInstance), 0,
	)
	if hwndR == 0 {
		return fmt.Errorf("CreateWindowExW: %w", err)
	}
	app.hwnd = windows.Handle(hwndR)
	defer procDestroyWindow.Call(uintptr(app.hwnd))

	if err := app.addTrayIcon(); err != nil {
		return err
	}
	defer app.removeTrayIcon()
	app.syncIconState() // correct the icon if starting out disabled

	procSetTimer.Call(uintptr(app.hwnd), iconSyncTimerID, iconSyncIntervalMs, 0)
	defer procKillTimer.Call(uintptr(app.hwnd), iconSyncTimerID)

	hook := newKeyboardHook(requests)
	if err := hook.install(); err != nil {
		messageBoxError(
			fmt.Sprintf("Failed to install the keyboard hook for Win+1..9:\n%v", err),
			appName,
		)
		return err
	}
	app.hook = hook
	defer hook.uninstall()

	app.messageLoop()
	return nil
}

// loadNamedIcon loads a 16x16 icon by resource name from this executable's
// own module (as embedded via go-winres; see winres/winres.json), falling
// back to the stock application icon if that ever fails.
func loadNamedIcon(hInstance windows.Handle, name string) windows.Handle {
	r0, _, _ := procLoadImageW.Call(
		uintptr(hInstance),
		uintptr(unsafe.Pointer(mustUTF16Ptr(name))),
		uintptr(imageIcon),
		16, 16,
		0,
	)
	if r0 != 0 {
		return windows.Handle(r0)
	}

	stockR, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
	return windows.Handle(stockR)
}

func (app *trayApp) messageLoop() {
	var m msg
	for {
		r0, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		ret := int32(r0)
		if ret <= 0 {
			// 0 == WM_QUIT, -1 == error; either way, stop pumping.
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (app *trayApp) wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch uint32(message) {
	case wmTrayCallback:
		switch uint32(lParam) {
		case wmLButtonUp:
			app.toggleEnabled()
		case wmRButtonUp, wmContextMenu:
			app.showMenu()
		}
		return 0
	case wmCommand:
		app.handleCommand(loword(uint32(wParam)))
		return 0
	case wmTimer:
		if wParam == iconSyncTimerID {
			app.syncIconState()
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r0, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r0
}

func (app *trayApp) handleCommand(id uint16) {
	switch id {
	case menuIDEnable:
		app.setEnabled(true)
	case menuIDDisable:
		app.setEnabled(false)
	case menuIDConfigure:
		app.openConfigure()
	case menuIDExit:
		procDestroyWindow.Call(uintptr(app.hwnd))
	}
}

func (app *trayApp) toggleEnabled() {
	app.setEnabled(!hotkeysEnabled.Load())
}

// setEnabled updates the shared enabled state (checked by the keyboard
// hook), persists it to config.yaml so the tray and the file never
// disagree, and refreshes the tray icon's graphic immediately.
func (app *trayApp) setEnabled(enabled bool) {
	hotkeysEnabled.Store(enabled)
	if err := setEnabledInFile(enabled); err != nil {
		verb := "Enabled"
		if !enabled {
			verb = "Disabled"
		}
		messageBoxError(fmt.Sprintf("%s, but failed to save it to the config file:\n%v", verb, err), appName)
	}
	app.syncIconState()
}

// openConfigure makes sure the config file exists, then opens it in
// whatever program is associated with .yaml files.
func (app *trayApp) openConfigure() {
	path, err := ensureConfigFile()
	if err != nil {
		messageBoxError(fmt.Sprintf("Couldn't create the config file:\n%v", err), appName)
		return
	}

	r0, _, _ := procShellExecuteW.Call(
		uintptr(app.hwnd),
		uintptr(unsafe.Pointer(mustUTF16Ptr("open"))),
		uintptr(unsafe.Pointer(mustUTF16Ptr(path))),
		0,
		0,
		uintptr(swShowNormal),
	)
	if r0 <= 32 { // ShellExecute returns a value > 32 on success.
		messageBoxError(fmt.Sprintf("Couldn't open an editor for:\n%s", path), appName)
	}
}

func (app *trayApp) addTrayIcon() error {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = app.hwnd
	nid.uID = trayIconID
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayCallback
	nid.hIcon = app.hIconEnabled
	setTip(&nid, fmt.Sprintf("%s %s", appName, version))

	r0, _, _ := procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid)))
	if r0 == 0 {
		return fmt.Errorf("Shell_NotifyIconW(NIM_ADD) failed")
	}
	return nil
}

func (app *trayApp) removeTrayIcon() {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = app.hwnd
	nid.uID = trayIconID
	procShellNotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&nid)))
}

// syncIconState swaps the tray icon's graphic to match hotkeysEnabled,
// whatever last changed it -- a tray click, the menu, or an external edit
// to config.yaml picked up by the background watcher. It's cheap to call
// often: it only touches the shell when the state actually changed.
func (app *trayApp) syncIconState() {
	enabled := hotkeysEnabled.Load()
	if app.iconStateInit && enabled == app.lastEnabled {
		return
	}
	app.iconStateInit = true
	app.lastEnabled = enabled

	icon := app.hIconEnabled
	if !enabled {
		icon = app.hIconDisabled
	}

	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = app.hwnd
	nid.uID = trayIconID
	nid.uFlags = nifIcon
	nid.hIcon = icon
	procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&nid)))
}

func (app *trayApp) showMenu() {
	hMenuR, _, _ := procCreatePopupMenu.Call()
	if hMenuR == 0 {
		debugLogf("CreatePopupMenu failed")
		return
	}
	hMenu := windows.Handle(hMenuR)
	defer procDestroyMenu.Call(uintptr(hMenu))

	enableFlags, disableFlags := uintptr(mfString), uintptr(mfString)
	if hotkeysEnabled.Load() {
		enableFlags = mfString | mfGrayed
	} else {
		disableFlags = mfString | mfGrayed
	}

	procAppendMenuW.Call(uintptr(hMenu), enableFlags, uintptr(menuIDEnable), uintptr(unsafe.Pointer(mustUTF16Ptr("Enable"))))
	procAppendMenuW.Call(uintptr(hMenu), disableFlags, uintptr(menuIDDisable), uintptr(unsafe.Pointer(mustUTF16Ptr("Disable"))))
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfString), uintptr(menuIDConfigure), uintptr(unsafe.Pointer(mustUTF16Ptr("Configure"))))
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfString), uintptr(menuIDExit), uintptr(unsafe.Pointer(mustUTF16Ptr("Exit"))))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Required so the menu closes correctly when the user clicks away from
	// it; see the Win32 docs for Shell_NotifyIcon / TrackPopupMenu.
	procSetForegroundWnd.Call(uintptr(app.hwnd))
	r0, _, _ := procTrackPopupMenu.Call(
		uintptr(hMenu),
		uintptr(tpmRightButton),
		uintptr(pt.X),
		uintptr(pt.Y),
		uintptr(app.hwnd),
		0,
	)
	if r0 == 0 {
		debugLogf("TrackPopupMenuEx failed")
	}
	procPostMessageW.Call(uintptr(app.hwnd), 0, 0, 0)
}

func setTip(nid *notifyIconDataW, tip string) {
	u16, err := syscall.UTF16FromString(tip)
	if err != nil {
		return
	}
	n := copy(nid.szTip[:], u16)
	if n == len(nid.szTip) {
		nid.szTip[len(nid.szTip)-1] = 0
	}
}
