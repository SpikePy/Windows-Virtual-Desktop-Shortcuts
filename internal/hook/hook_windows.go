//go:build windows

// Package hook installs the low-level keyboard and mouse hooks that
// intercept this program's shortcuts before the focused application (or,
// for the mouse wheel, the one under the pointer) sees them.
//
// RegisterHotKey isn't used because a Ctrl+Alt hotkey would also fire for
// AltGr, which layouts like German use to type characters such as '{'
// (AltGr+7). A WH_KEYBOARD_LL hook can tell the two apart by which Alt key
// is held, and swallows a shortcut's key by returning a non-zero value
// from the hook procedure.
package hook

import (
	"fmt"
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
	whMouseLL    = 14
	hcAction     = 0

	wmMouseWheel = 0x020A

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

// msllhookstruct mirrors the Win32 MSLLHOOKSTRUCT layout used by
// WH_MOUSE_LL hook callbacks.
type msllhookstruct struct {
	X, Y        int32
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Hook is a pair of installed keyboard and mouse hooks. Create one with
// New and install it on the thread that runs the message loop.
type Hook struct {
	handle        windows.Handle
	mouseHandle   windows.Handle
	requests      chan<- hotkeys.Request
	enabled       func() bool
	callback      uintptr
	mouseCallback uintptr

	// wheel is only touched by mouseProc, which always runs on the thread
	// that installed the hook.
	wheel hotkeys.Wheel
}

// New returns a hook that sends recognised shortcuts to requests, and asks
// enabled before acting on any of them, so the tray's Enable/Disable can
// switch them off without uninstalling anything.
func New(requests chan<- hotkeys.Request, enabled func() bool) *Hook {
	h := &Hook{requests: requests, enabled: enabled}
	h.callback = syscall.NewCallback(h.proc)
	h.mouseCallback = syscall.NewCallback(h.mouseProc)
	return h
}

// Install registers both hooks with Windows. It must be called on a
// thread that pumps a message loop, and that thread must keep running for
// the hooks to keep receiving input.
func (h *Hook) Install() error {
	r0, _, err := procSetWindowsHookExW.Call(whKeyboardLL, h.callback, 0, 0)
	if r0 == 0 {
		return fmt.Errorf("keyboard hook: %w", err)
	}
	h.handle = windows.Handle(r0)

	r0, _, err = procSetWindowsHookExW.Call(whMouseLL, h.mouseCallback, 0, 0)
	if r0 == 0 {
		h.Uninstall()
		return fmt.Errorf("mouse hook: %w", err)
	}
	h.mouseHandle = windows.Handle(r0)
	return nil
}

// Uninstall removes the hooks. Safe to call if they were never installed.
func (h *Hook) Uninstall() {
	for _, handle := range []*windows.Handle{&h.handle, &h.mouseHandle} {
		if *handle != 0 {
			procUnhookWindowsHookEx.Call(uintptr(*handle))
			*handle = 0
		}
	}
}

func callNext(handle windows.Handle, nCode, wParam, lParam uintptr) uintptr {
	r0, _, _ := procCallNextHookEx.Call(uintptr(handle), nCode, wParam, lParam)
	return r0
}

func (h *Hook) callNext(nCode, wParam, lParam uintptr) uintptr {
	return callNext(h.handle, nCode, wParam, lParam)
}

// send hands a request to the switcher without ever blocking a hook
// callback: if the switcher is still busy, the request is dropped.
func (h *Hook) send(req hotkeys.Request) {
	select {
	case h.requests <- req:
	default:
	}
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
		h.send(req)
	}
	return 1
}

// mouseProc is the WH_MOUSE_LL hook procedure. Like proc it must return
// quickly. It only looks at vertical wheel events: Ctrl+Alt+wheel is
// swallowed so the window under the pointer doesn't scroll or zoom, and
// turns into a previous/next desktop request once a full notch has built
// up.
func (h *Hook) mouseProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) != hcAction || wParam != wmMouseWheel || !h.enabled() {
		return callNext(h.mouseHandle, nCode, wParam, lParam)
	}

	ms := (*msllhookstruct)(unsafe.Pointer(lParam))
	delta := int(int16(ms.MouseData >> 16))
	req, fire, ok := h.wheel.Scroll(delta, modifiers())
	if !ok {
		return callNext(h.mouseHandle, nCode, wParam, lParam)
	}
	if fire {
		h.send(req)
	}
	return 1
}
