//go:build windows

package main

import (
	"fmt"
	"os"
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

	appName   = "Virtual Desktop Shortcuts"
	className = "VDSwitcherHiddenWindowClass"
)

// trayApp owns the hidden window, tray icon and keyboard hook. Everything
// here must run on a single, dedicated OS thread: Win32 windows, hooks and
// message loops are all thread-affine.
type trayApp struct {
	hInstance windows.Handle
	hwnd      windows.Handle
	hIcon     windows.Handle
	hook      *keyboardHook
}

// runApp registers a hidden window, adds a tray icon, installs the
// low-level keyboard hook and pumps the Windows message loop until the
// user picks "Exit" from the tray menu or the window is otherwise
// destroyed. It must be called from a goroutine that has called
// runtime.LockOSThread and will not unlock it until this function returns.
func runApp(requests chan<- desktopRequest) error {
	hInstanceR, _, _ := procGetModuleHandleW.Call(0)
	app := &trayApp{hInstance: windows.Handle(hInstanceR)}

	app.hIcon = loadAppIcon()
	cursorR, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))

	classNamePtr := mustUTF16Ptr(className)
	wndProcPtr := syscall.NewCallback(app.wndProc)

	var wc wndClassExW
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	wc.style = csHRedraw | csVRedraw
	wc.lpfnWndProc = wndProcPtr
	wc.hInstance = app.hInstance
	wc.hIcon = app.hIcon
	wc.hIconSm = app.hIcon
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

// loadAppIcon extracts the icon embedded in this executable's own resources
// (via go-winres, see winres/winres.json and rsrc_windows_amd64.syso) so
// the tray icon matches the one shown for the .exe file in Explorer. Index
// 0 is safe to hardcode: the exe embeds exactly one icon group, so it's
// unambiguous regardless of what resource ID/name go-winres assigned it.
// Falls back to the stock application icon if that ever fails.
func loadAppIcon() windows.Handle {
	if exePath, err := os.Executable(); err == nil {
		var hIconLarge, hIconSmall windows.Handle
		r0, _, _ := procExtractIconExW.Call(
			uintptr(unsafe.Pointer(mustUTF16Ptr(exePath))),
			0,
			uintptr(unsafe.Pointer(&hIconLarge)),
			uintptr(unsafe.Pointer(&hIconSmall)),
			1,
		)
		if int32(r0) > 0 && hIconSmall != 0 {
			return hIconSmall
		}
	}

	iconR, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
	return windows.Handle(iconR)
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
		case wmLButtonUp, wmRButtonUp, wmContextMenu:
			app.showMenu()
		}
		return 0
	case wmCommand:
		app.handleCommand(loword(uint32(wParam)))
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
		hotkeysEnabled.Store(true)
		if err := setEnabledInFile(true); err != nil {
			messageBoxError(fmt.Sprintf("Enabled, but failed to save it to the config file:\n%v", err), appName)
		}
	case menuIDDisable:
		hotkeysEnabled.Store(false)
		if err := setEnabledInFile(false); err != nil {
			messageBoxError(fmt.Sprintf("Disabled, but failed to save it to the config file:\n%v", err), appName)
		}
	case menuIDConfigure:
		app.openConfigure()
	case menuIDExit:
		procDestroyWindow.Call(uintptr(app.hwnd))
	}
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
	nid.hIcon = app.hIcon
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

func (app *trayApp) showMenu() {
	hMenuR, _, _ := procCreatePopupMenu.Call()
	if hMenuR == 0 {
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
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfString), uintptr(menuIDConfigure), uintptr(unsafe.Pointer(mustUTF16Ptr("Configure"))))
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(uintptr(hMenu), uintptr(mfString), uintptr(menuIDExit), uintptr(unsafe.Pointer(mustUTF16Ptr("Exit"))))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Required so the menu closes correctly when the user clicks away from
	// it; see the Win32 docs for Shell_NotifyIcon / TrackPopupMenu.
	procSetForegroundWnd.Call(uintptr(app.hwnd))
	procTrackPopupMenu.Call(
		uintptr(hMenu),
		uintptr(tpmRightButton),
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(app.hwnd),
		0,
	)
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
