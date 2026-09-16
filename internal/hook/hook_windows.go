//go:build windows

// Package hook installs the low-level keyboard hook that intercepts this
// program's shortcuts before the focused application sees them.
//
// RegisterHotKey isn't used because a Ctrl+Alt hotkey would also fire for
// AltGr, which layouts like German use to type characters such as '{'
// (AltGr+7). A WH_KEYBOARD_LL hook can tell the two apart by which Alt key
// is held, and swallows a shortcut's key by returning a non-zero value
// from the hook procedure.
package hook

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/hotkeys"
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")

	procSetWindowsHookExW   = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = modUser32.NewProc("CallNextHookEx")
	procGetAsyncKeyState    = modUser32.NewProc("GetAsyncKeyState")
)

const (
	whKeyboardLL = 13
	hcAction     = 0

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	vkShift   = 0x10
	vkControl = 0x11
	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkLMenu   = 0xA4 // left Alt
	vkRMenu   = 0xA5 // right Alt (AltGr on some layouts)
)

// kbdllhookstruct mirrors the Win32 KBDLLHOOKSTRUCT layout used by
// WH_KEYBOARD_LL hook callbacks.
type kbdllhookstruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Hook is an installed keyboard hook. Create one with New and install it
// on the thread that runs the message loop.
type Hook struct {
	handle   windows.Handle
	requests chan<- hotkeys.Request
	enabled  func() bool
	callback uintptr
}

// New returns a hook that sends recognised shortcuts to requests, and asks
// enabled before acting on any of them, so the tray's Enable/Disable can
// switch them off without uninstalling anything.
func New(requests chan<- hotkeys.Request, enabled func() bool) *Hook {
	h := &Hook{requests: requests, enabled: enabled}
	h.callback = syscall.NewCallback(h.proc)
	return h
}

// Install registers the hook with Windows. It must be called on a thread
// that pumps a message loop, and that thread must keep running for the
// hook to keep receiving keys.
func (h *Hook) Install() error {
	r0, _, err := procSetWindowsHookExW.Call(whKeyboardLL, h.callback, 0, 0)
	if r0 == 0 {
		return err
	}
	h.handle = windows.Handle(r0)
	return nil
}

// Uninstall removes the hook. Safe to call if it was never installed.
func (h *Hook) Uninstall() {
	if h.handle != 0 {
		procUnhookWindowsHookEx.Call(uintptr(h.handle))
		h.handle = 0
	}
}

func (h *Hook) callNext(nCode, wParam, lParam uintptr) uintptr {
	r0, _, _ := procCallNextHookEx.Call(uintptr(h.handle), nCode, wParam, lParam)
	return r0
}

func keyDown(vk int) bool {
	r0, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r0&0x8000 != 0
}

// modifiers reads the modifier state Windows reports right now.
func modifiers() hotkeys.Modifiers {
	return hotkeys.Modifiers{
		Ctrl:     keyDown(vkControl),
		LeftAlt:  keyDown(vkLMenu),
		RightAlt: keyDown(vkRMenu),
		Shift:    keyDown(vkShift),
		Win:      keyDown(vkLWin) || keyDown(vkRWin),
	}
}

// proc is the WH_KEYBOARD_LL hook procedure. It must return quickly:
// Windows ignores a hook that runs past LowLevelHooksTimeout and passes
// the key through anyway. So it only classifies the keystroke and hands
// the request off over a non-blocking channel send before swallowing both
// the key's key-down and key-up.
func (h *Hook) proc(nCode, wParam, lParam uintptr) uintptr {
	isDown := wParam == wmKeyDown || wParam == wmSysKeyDown
	isUp := wParam == wmKeyUp || wParam == wmSysKeyUp

	if int32(nCode) != hcAction || (!isDown && !isUp) || !h.enabled() {
		return h.callNext(nCode, wParam, lParam)
	}

	kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))
	req, ok := hotkeys.For(kb.VkCode, modifiers())
	if !ok {
		return h.callNext(nCode, wParam, lParam)
	}

	if isDown {
		select {
		case h.requests <- req:
		default:
			// Switcher is busy; drop the request rather than blocking
			// this hook callback.
		}
	}
	return 1
}
