//go:build windows

package main

import (
	"fmt"
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
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procSendInput           = modUser32.NewProc("SendInput")
	procFindWindowW         = modUser32.NewProc("FindWindowW")
	procGetWindowThreadPID  = modUser32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput   = modUser32.NewProc("AttachThreadInput")

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
	procLoadImageW       = modUser32.NewProc("LoadImageW")
	procLoadCursorW      = modUser32.NewProc("LoadCursorW")
	procCreatePopupMenu  = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = modUser32.NewProc("AppendMenuW")
	procDestroyMenu      = modUser32.NewProc("DestroyMenu")
	procTrackPopupMenu   = modUser32.NewProc("TrackPopupMenuEx")
	procSetForegroundWnd = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos     = modUser32.NewProc("GetCursorPos")
	procMessageBoxW      = modUser32.NewProc("MessageBoxW")
	procSetTimer         = modUser32.NewProc("SetTimer")
	procKillTimer        = modUser32.NewProc("KillTimer")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = modShell32.NewProc("ShellExecuteW")

	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")

	procGetModuleHandleW   = modKernel32.NewProc("GetModuleHandleW")
	procOutputDebugStringW = modKernel32.NewProc("OutputDebugStringW")
)

const (
	hcAction = 0

	whKeyboardLL = 13

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkControl = 0x11
	vkShift   = 0x10
	vkMenu    = 0x12
	vk1       = 0x31
	vk9       = 0x39
	vkLeft    = 0x25
	vkRight   = 0x27
	// vkMenuMask is an unassigned virtual-key code, used purely as a
	// harmless keystroke to inject (see lowLevelKeyboardProc).
	vkMenuMask = 0xE8

	wmDestroy      = 0x0002
	wmCommand      = 0x0111
	wmTimer        = 0x0113
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmContextMenu  = 0x007B
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	idiApplication = 32512
	idcArrow       = 32512

	imageIcon = 1

	mfString    = 0x0000
	mfGrayed    = 0x00000001
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	swShowNormal = 1

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	mbIconError = 0x00000010
	mbOK        = 0x00000000

	clsctxLocalServer = 0x4

	inputKeyboard  = 1
	keyeventfKeyUp = 0x0002
)

type point struct {
	X, Y int32
}

// input mirrors the Win32 INPUT struct (winuser.h) for the keyboard
// (INPUT_KEYBOARD) case only, laid out to match the real x64 ABI size (40
// bytes: an 8-byte header, unioned with up to a 32-byte MOUSEINPUT) even
// though only the KEYBDINPUT fields are ever populated -- SendInput
// validates the caller's struct size against its own sizeof(INPUT) and
// fails outright on a mismatch.
type input struct {
	inputType uint32
	_         uint32 // pad to 8-byte-align the union, matching the C layout
	wVk       uint16
	wScan     uint16
	dwFlags   uint32
	time      uint32
	extraInfo uintptr
	_         [8]byte // pad the union out to MOUSEINPUT's size
}

// sendKeyTap injects a key-down followed by a key-up for vk via SendInput.
func sendKeyTap(vk uint16) {
	ins := [2]input{
		{inputType: inputKeyboard, wVk: vk},
		{inputType: inputKeyboard, wVk: vk, dwFlags: keyeventfKeyUp},
	}
	procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
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

// debugLogf sends a formatted message to the system debug output (visible
// via DebugView or an attached debugger). This app has no console/UI for
// routine, non-fatal errors, so this is the only way to diagnose them
// without popping up a message box on every failure.
func debugLogf(format string, args ...any) {
	msg := fmt.Sprintf("[%s] %s\n", appName, fmt.Sprintf(format, args...))
	procOutputDebugStringW.Call(uintptr(unsafe.Pointer(mustUTF16Ptr(msg))))
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

// getForegroundWindow returns the HWND of the currently focused top-level
// window, or 0 if there is none.
func getForegroundWindow() uintptr {
	r0, _, _ := procGetForegroundWindow.Call()
	return r0
}

// desktopWindow returns Explorer's desktop window ("Progman"), which is
// shown on every virtual desktop and belongs to no app, or 0 if it can't
// be found.
func desktopWindow() uintptr {
	r0, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(mustUTF16Ptr("Progman"))), 0)
	return r0
}

// activateWindow makes hwnd the foreground window and reports whether that
// worked. Windows normally only lets the app that owns the foreground
// window hand focus elsewhere, so this briefly attaches the calling
// thread to that window's input queue first, the usual workaround.
func activateWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	fg := getForegroundWindow()
	if fg == hwnd {
		return true
	}
	self := uintptr(windows.GetCurrentThreadId())
	if fg != 0 {
		fgThread, _, _ := procGetWindowThreadPID.Call(fg, 0)
		if fgThread != 0 && fgThread != self {
			procAttachThreadInput.Call(self, fgThread, 1)
			defer procAttachThreadInput.Call(self, fgThread, 0)
		}
	}
	r0, _, _ := procSetForegroundWnd.Call(hwnd)
	return r0 != 0
}
