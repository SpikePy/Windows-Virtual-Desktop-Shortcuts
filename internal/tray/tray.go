//go:build windows

// Package tray provides a minimal Shell_NotifyIcon-based system tray icon
// with a right-click popup menu, independent of any GUI toolkit.
package tray

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/win32"
)

var (
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modShell32 = windows.NewLazySystemDLL("shell32.dll")

	procGetCursorPos           = modUser32.NewProc("GetCursorPos")
	procCreatePopupMenu        = modUser32.NewProc("CreatePopupMenu")
	procDestroyMenu            = modUser32.NewProc("DestroyMenu")
	procAppendMenuW            = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenuEx       = modUser32.NewProc("TrackPopupMenuEx")
	procRegisterWindowMessageW = modUser32.NewProc("RegisterWindowMessageW")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
)

const (
	wsOverlappedWindow = 0x00000000

	wmNull      = 0x0000
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString    = 0x00000000
	mfChecked   = 0x00000008
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNoAnimation = 0x4000
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconDataW struct {
	cbSize            uint32
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          guid
	hBalloonIcon      uintptr
}

type point struct{ X, Y int32 }

const trayWindowClassName = "VDSwitcherTrayHiddenWindow"

// Everything below is only touched on the thread that created the tray
// window and runs its message loop (NewWindow, SetIcon, RemoveIcon, and
// wndProcCB, which that loop dispatches), so no locking is needed.
var (
	onLeftClick     func()
	onRightClick    func()
	onConfigChanged func()

	// taskbarCreated is the "TaskbarCreated" message Explorer broadcasts
	// whenever it (re)starts. By then every tray icon it showed is gone,
	// and each app has to add its own back.
	taskbarCreated uint32

	// shown is the icon SetIcon was last asked to show, kept so it can be
	// re-added after an Explorer restart.
	shown struct {
		hwnd, hIcon uintptr
		tooltip     string
		added       bool
	}

	wndProcCB = syscall.NewCallback(func(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
		switch {
		case message == win32.WMTrayCallback && (lParam == wmLButtonUp || lParam == wmRButtonUp):
			cb := onLeftClick
			if lParam == wmRButtonUp {
				cb = onRightClick
			}
			if cb != nil {
				cb()
			}
			return 0
		case message == win32.WMConfigChanged:
			if onConfigChanged != nil {
				onConfigChanged()
			}
			return 0
		case taskbarCreated != 0 && message == taskbarCreated:
			if shown.hwnd != 0 {
				shown.added = false
				SetIcon(shown.hwnd, shown.hIcon, shown.tooltip) // nobody to report a failure to; the next SetIcon retries
			}
			return 0
		}
		return win32.DefWindowProc(hwnd, message, wParam, lParam)
	})
)

// NewWindow creates a hidden window that owns the tray icon and any popup
// menu, and wires the left-click, right-click and config-changed
// callbacks. It must be created on, and its messages pumped from, the same
// OS thread for the lifetime of the program (see runtime.LockOSThread in
// main).
func NewWindow(title string, left, right, configChanged func()) (uintptr, error) {
	onLeftClick, onRightClick, onConfigChanged = left, right, configChanged
	if r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(win32.UTF16Ptr("TaskbarCreated")))); r != 0 {
		taskbarCreated = uint32(r)
	}

	if err := win32.RegisterClass(trayWindowClassName, wndProcCB, 0); err != nil {
		return 0, err
	}
	// Deliberately never shown (no ShowWindow call) - it exists only to
	// own the notify icon and receive its callback messages. It must stay
	// a normal top-level window, not a message-only one, to receive the
	// TaskbarCreated broadcast.
	return win32.CreateWindow(0, wsOverlappedWindow, trayWindowClassName, title,
		win32.CWUseDefault, win32.CWUseDefault, win32.CWUseDefault, win32.CWUseDefault)
}

// DestroyWindow destroys a window created by NewWindow. Safe to call on a
// zero handle.
func DestroyWindow(hwnd uintptr) { win32.DestroyWindow(hwnd) }

func newNotifyIconData(hwnd, hIcon uintptr, tooltip string) notifyIconDataW {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = win32.WMTrayCallback
	nid.hIcon = hIcon
	tip := windows.StringToUTF16(tooltip)
	n := copy(nid.szTip[:], tip)
	if n == len(nid.szTip) {
		nid.szTip[len(nid.szTip)-1] = 0
	}
	return nid
}

// SetIcon shows hIcon with tooltip as the tray icon of hwnd (from
// NewWindow), adding it the first time and updating it after that. The
// icon is remembered and re-added automatically whenever Explorer
// restarts, so the caller must keep hIcon alive until it passes a new one.
func SetIcon(hwnd, hIcon uintptr, tooltip string) error {
	shown.hwnd, shown.hIcon, shown.tooltip = hwnd, hIcon, tooltip
	nid := newNotifyIconData(hwnd, hIcon, tooltip)
	if shown.added {
		if r, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid))); r != 0 {
			return nil
		}
		// The icon has gone missing (e.g. Explorer restarted and the
		// broadcast was missed); add it afresh below.
	}
	if r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		shown.added = false
		return fmt.Errorf("Shell_NotifyIconW(NIM_ADD): %w", e)
	}
	shown.added = true
	return nil
}

// RemoveIcon removes the tray icon and stops it from being re-added. Safe
// to call on a zero hwnd.
func RemoveIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	shown.hwnd, shown.hIcon, shown.tooltip, shown.added = 0, 0, "", false
}

// MenuItem is one entry in the popup menu shown by ShowMenu. ID 0 is
// reserved (means "nothing selected"); a zero-value MenuItem renders as a
// separator.
type MenuItem struct {
	ID      uint32
	Label   string
	Checked bool // shows a checkmark, e.g. on the state that's active
}

// ShowMenu displays a popup menu at the current cursor position, owned by
// hwnd (from NewWindow), and blocks until the user picks an item or
// dismisses it. Returns the selected item's ID, or 0 if none was chosen.
func ShowMenu(hwnd uintptr, items []MenuItem) uint32 {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return 0
	}
	defer procDestroyMenu.Call(hMenu)

	for _, it := range items {
		if it.Label == "" {
			procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Checked {
			flags |= mfChecked
		}
		procAppendMenuW.Call(hMenu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(win32.UTF16Ptr(it.Label))))
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Required so the menu reliably closes when the user clicks away from
	// it (documented Win32 tray-icon idiom).
	win32.SetForegroundWindow(hwnd)

	id, _, _ := procTrackPopupMenuEx.Call(
		hMenu,
		uintptr(tpmRightButton|tpmReturnCmd|tpmNoAnimation),
		uintptr(pt.X), uintptr(pt.Y),
		hwnd, 0,
	)

	win32.PostMessage(hwnd, wmNull, 0, 0)
	return uint32(id)
}
