//go:build windows

// Package win32 holds the raw Win32 declarations and helpers the tray and
// app packages need - window-class registration, window creation, the
// message loop - plus every private message number this program defines,
// kept in one place so they can never collide.
package win32

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")

	procMessageBoxW   = modUser32.NewProc("MessageBoxW")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")

	procGetModuleHandleW    = modKernel32.NewProc("GetModuleHandleW")
	procLoadCursorW         = modUser32.NewProc("LoadCursorW")
	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW    = modUser32.NewProc("DispatchMessageW")
)

// Message numbers from WM_APP upward are free for application use.
const (
	wmApp = 0x8000

	// WMTrayCallback is sent to the tray icon's window when the icon is
	// clicked.
	WMTrayCallback = wmApp + 1

	// WMConfigChanged is posted to the tray window when the config file
	// watcher sees the enabled setting change on disk, so the icon is
	// refreshed on the thread that owns it.
	WMConfigChanged = wmApp + 2
)

// CWUseDefault is CW_USEDEFAULT, for CreateWindow's position and size.
const CWUseDefault int32 = -0x80000000

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// ModuleHandle returns this exe's HINSTANCE.
func ModuleHandle() syscall.Handle {
	r, _, _ := procGetModuleHandleW.Call(0)
	return syscall.Handle(r)
}

// UTF16Ptr converts s to a NUL-terminated UTF-16 string for a Win32 call.
// It panics if s contains a NUL byte, so it's only for this program's own
// fixed strings, never for user input.
func UTF16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

// RegisterClass registers a window class with the given window procedure
// (from syscall.NewCallback) and background brush (0 for none), using the
// normal arrow cursor.
func RegisterClass(name string, wndProc uintptr, background syscall.Handle) error {
	const idcArrow = 32512
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassExW{
		hCursor:       syscall.Handle(cursor),
		lpfnWndProc:   wndProc,
		hInstance:     ModuleHandle(),
		hbrBackground: background,
		lpszClassName: UTF16Ptr(name),
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW: %w", e)
	}
	return nil
}

// CreateWindow creates, but doesn't show, a window of a class registered
// with RegisterClass.
func CreateWindow(exStyle, style uint32, class, title string, x, y, width, height int32) (uintptr, error) {
	hwnd, _, e := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(UTF16Ptr(class))),
		uintptr(unsafe.Pointer(UTF16Ptr(title))),
		uintptr(style),
		intArg(x), intArg(y), intArg(width), intArg(height),
		0, 0, uintptr(ModuleHandle()), 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", e)
	}
	return hwnd, nil
}

// intArg passes a possibly negative int32 (CWUseDefault) in a syscall
// argument slot, going via int64 so the sign survives regardless of
// pointer width.
func intArg(v int32) uintptr { return uintptr(int64(v)) }

// DestroyWindow destroys hwnd. Safe to call on 0.
func DestroyWindow(hwnd uintptr) {
	if hwnd != 0 {
		procDestroyWindow.Call(hwnd)
	}
}

// SetForegroundWindow brings hwnd to the foreground.
func SetForegroundWindow(hwnd uintptr) { procSetForegroundWindow.Call(hwnd) }

// DefWindowProc is the default window procedure, for every message a
// window procedure doesn't handle itself.
func DefWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}

// PostMessage posts msg to hwnd's queue, or to the calling thread's own
// queue if hwnd is 0. Safe to call from any goroutine.
func PostMessage(hwnd uintptr, msg uint32, wParam, lParam uintptr) {
	procPostMessageW.Call(hwnd, uintptr(msg), wParam, lParam)
}

// Quit asks the message loop on the calling thread to return.
func Quit() { procPostQuitMessage.Call(0) }

const (
	mbOK        = 0x00000000
	mbIconError = 0x00000010

	swShowNormal = 1
)

// ErrorBox shows a modal error message. This program has no window of its
// own to report through, so a failure the user has to know about (the
// keyboard hook not installing, say) goes here.
func ErrorBox(title, text string) {
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(UTF16Ptr(text))),
		uintptr(unsafe.Pointer(UTF16Ptr(title))),
		mbOK|mbIconError)
}

// OpenWithDefaultApp opens path in whatever application Windows has
// associated with its file type.
func OpenWithDefaultApp(hwnd uintptr, path string) error {
	r, _, _ := procShellExecuteW.Call(hwnd,
		uintptr(unsafe.Pointer(UTF16Ptr("open"))),
		uintptr(unsafe.Pointer(UTF16Ptr(path))),
		0, 0, swShowNormal)
	// ShellExecute returns a value greater than 32 on success.
	if r <= 32 {
		return fmt.Errorf("ShellExecuteW(%s): %d", path, r)
	}
	return nil
}

// RunMessageLoop pumps messages until Quit is called (or the window is
// destroyed), dispatching each to its window procedure. It must run on the
// same OS thread that created the windows it serves.
func RunMessageLoop() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
