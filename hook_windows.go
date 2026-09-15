//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// keyboardHook manages the low-level keyboard hook used to intercept
// Win+1..Win+9 (switch to desktop N) and Win+Shift+1..Win+Shift+9 (move
// the foreground window to desktop N).
//
// RegisterHotKey cannot be used here: Win+<digit> is reserved by the shell
// for launching pinned taskbar apps, and the OS refuses to let normal
// applications register it as a hotkey. A WH_KEYBOARD_LL hook sees the
// keystroke first and can swallow it by returning a non-zero value from
// the hook procedure. That keeps the digit away from the focused app, but
// Explorer still reacts to Win+<digit> on its own, so it also has to be
// told not to (DisabledHotkeys, see the README).
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
// quickly: it only classifies the keystroke and, if it is a plain
// Win+<digit> or Win+Shift+<digit> combination, hands the request off to
// the switcher goroutine over a non-blocking channel send before
// swallowing both the digit's key-down and key-up.
func (h *keyboardHook) lowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	isDown := wParam == wmKeyDown || wParam == wmSysKeyDown
	isUp := wParam == wmKeyUp || wParam == wmSysKeyUp

	if int32(nCode) != hcAction || (!isDown && !isUp) {
		return h.callNext(nCode, wParam, lParam)
	}
	kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))

	if !hotkeysEnabled.Load() || kb.VkCode < vk1 || kb.VkCode > vk9 {
		return h.callNext(nCode, wParam, lParam)
	}
	winDown := isKeyDown(vkLWin) || isKeyDown(vkRWin)
	// Ctrl and Alt are left alone so other Win+Ctrl/Win+Alt combinations
	// keep working; Shift toggles switch vs. move.
	if !winDown || isKeyDown(vkControl) || isKeyDown(vkMenu) {
		return h.callNext(nCode, wParam, lParam)
	}

	if isDown {
		req := desktopRequest{
			action: actionSwitchToDesktop,
			index:  int(kb.VkCode - vk1), // 0-based
		}
		if isKeyDown(vkShift) {
			req.action = actionMoveWindowToDesktop
		}
		select {
		case h.requests <- req:
		default:
			// Switcher is busy; drop the request rather than blocking
			// this hook callback.
		}
		// With the digit swallowed, Windows would see Win pressed and
		// released on its own and open the Start menu. Tapping an
		// unassigned key while Win is still held prevents that (the same
		// "menu mask key" trick AutoHotkey uses) without touching the Win
		// key's own key-up.
		sendKeyTap(vkMenuMask)
	}
	return 1
}
