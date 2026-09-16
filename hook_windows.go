//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// keyboardHook manages the low-level keyboard hook used to intercept
// Ctrl+Alt+1..9 (switch to desktop N), Ctrl+Alt+Left/Right (switch to the
// previous/next desktop), and the same with Shift (move the foreground
// window there instead).
//
// RegisterHotKey isn't used because a Ctrl+Alt hotkey would also fire for
// AltGr, which layouts like German use to type characters such as '{'
// (AltGr+7). A WH_KEYBOARD_LL hook can tell the two apart by which Alt key
// is held, and swallows the shortcut's key by returning a non-zero value
// from the hook procedure.
type keyboardHook struct {
	handle   windows.Handle
	requests chan<- desktopRequest
	callback uintptr
}

func newKeyboardHook(requests chan<- desktopRequest) *keyboardHook {
	h := &keyboardHook{requests: requests}
	h.callback = syscall.NewCallback(h.lowLevelKeyboardProc)
	return h
}

func (h *keyboardHook) install() error {
	r0, _, err := procSetWindowsHookExW.Call(
		uintptr(whKeyboardLL),
		h.callback,
		0,
		0,
	)
	if r0 == 0 {
		return err
	}
	h.handle = windows.Handle(r0)
	return nil
}

func (h *keyboardHook) uninstall() {
	if h.handle != 0 {
		procUnhookWindowsHookEx.Call(uintptr(h.handle))
		h.handle = 0
	}
}

func (h *keyboardHook) callNext(nCode, wParam, lParam uintptr) uintptr {
	r0, _, _ := procCallNextHookEx.Call(uintptr(h.handle), nCode, wParam, lParam)
	return r0
}

func isKeyDown(vk int) bool {
	r0, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r0&0x8000 != 0
}

// lowLevelKeyboardProc is the WH_KEYBOARD_LL hook procedure. It must return
// quickly: it only classifies the keystroke and, if it is one of the
// shortcuts (see shortcutFor), hands the request off to the switcher
// goroutine over a non-blocking channel send before swallowing both the
// key's key-down and key-up.
func (h *keyboardHook) lowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	isDown := wParam == wmKeyDown || wParam == wmSysKeyDown
	isUp := wParam == wmKeyUp || wParam == wmSysKeyUp

	if int32(nCode) != hcAction || (!isDown && !isUp) || !hotkeysEnabled.Load() {
		return h.callNext(nCode, wParam, lParam)
	}
	kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))

	req, ok := shortcutFor(kb.VkCode)
	if !ok || !shortcutModifiersHeld() {
		return h.callNext(nCode, wParam, lParam)
	}

	if isDown {
		if isKeyDown(vkShift) {
			req.action = actionMoveWindowToDesktop
		}
		select {
		case h.requests <- req:
		default:
			// Switcher is busy; drop the request rather than blocking
			// this hook callback.
		}
	}
	return 1
}

// shortcutModifiersHeld reports whether Ctrl and the left Alt key are held
// without the right Alt key or Win. On layouts with AltGr, the right Alt
// key also reports Ctrl as held, so excluding it keeps AltGr+digit typing
// characters as usual.
func shortcutModifiersHeld() bool {
	return isKeyDown(vkControl) && isKeyDown(vkLMenu) && !isKeyDown(vkRMenu) &&
		!isKeyDown(vkLWin) && !isKeyDown(vkRWin)
}

// shortcutFor maps a key pressed together with Ctrl+Alt to the desktop it
// targets: 1-9 pick a desktop by number, Left/Right the previous/next one.
func shortcutFor(vk uint32) (desktopRequest, bool) {
	switch {
	case vk >= vk1 && vk <= vk9:
		return desktopRequest{index: int(vk - vk1)}, true
	case vk == vkLeft:
		return desktopRequest{index: -1, relative: true}, true
	case vk == vkRight:
		return desktopRequest{index: 1, relative: true}, true
	}
	return desktopRequest{}, false
}
