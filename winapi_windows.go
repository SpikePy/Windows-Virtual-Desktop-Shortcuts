//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Lazy-loaded DLLs and procedures for the raw Win32 APIs that
// golang.org/x/sys/windows does not wrap (hooks, windows, menus, tray icon).
var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procSetWindowsHookExW   = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = modUser32.NewProc("CallNextHookEx")
	procGetAsyncKeyState    = modUser32.NewProc("GetAsyncKeyState")

	procRegisterClassExW = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procPostMessageW     = modUser32.NewProc("PostMessageW")
	procLoadIconW        = modUser32.NewProc("LoadIconW")
	procLoadCursorW      = modUser32.NewProc("LoadCursorW")
	procCreatePopupMenu  = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = modUser32.NewProc("AppendMenuW")
	procDestroyMenu      = modUser32.NewProc("DestroyMenu")
	procTrackPopupMenu   = modUser32.NewProc("TrackPopupMenuEx")
	procSetForegroundWnd = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos     = modUser32.NewProc("GetCursorPos")
	procMessageBoxW      = modUser32.NewProc("MessageBoxW")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")

	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")

	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")
)

const (
	hcAction = 0

	whKeyboardLL = 13

	wmKeyDown    = 0x0100
	wmSysKeyDown = 0x0104

	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkControl = 0x11
	vkShift   = 0x10
	vkMenu    = 0x12
	vk1       = 0x31
	vk9       = 0x39

	wmDestroy      = 0x0002
	wmCommand      = 0x0111
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmContextMenu  = 0x007B
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	idiApplication = 32512
	idcArrow       = 32512

	mfString = 0x0000

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	mbIconError = 0x00000010
	mbOK        = 0x00000000

	clsctxLocalServer = 0x4
)

type point struct {
	X, Y int32
}

type msg struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// kbdllhookstruct mirrors the Win32 KBDLLHOOKSTRUCT layout used by
// WH_KEYBOARD_LL hook callbacks.
type kbdllhookstruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// notifyIconDataW mirrors the modern (Vista+) NOTIFYICONDATAW layout.
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

func mustUTF16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func loword(v uint32) uint16 {
	return uint16(v & 0xFFFF)
}

func messageBoxError(text, title string) {
	procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(mustUTF16Ptr(text))),
		uintptr(unsafe.Pointer(mustUTF16Ptr(title))),
		uintptr(mbOK|mbIconError),
	)
}

// comCall invokes the vtable method at the given zero-based slot (0 =
// QueryInterface, 1 = AddRef, 2 = Release, 3+ = interface-specific methods)
// on a raw COM interface pointer, using the standard COM object layout where
// the first machine word at the object address points to its vtable.
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(obj)
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	full := make([]uintptr, 0, len(args)+1)
	full = append(full, uintptr(obj))
	full = append(full, args...)
	r0, _, _ := syscall.SyscallN(fn, full...)
	return r0
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

func hrFailed(hr uintptr) bool {
	return int32(hr) < 0
}
