//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// keyboardHook manages the low-level keyboard hook used to intercept
// Win+1..Win+9 before Explorer's taskbar handles them.
//
// RegisterHotKey cannot be used here: Win+<digit> is reserved by the shell
// for launching pinned taskbar apps, and the OS refuses to let normal
// applications register it as a hotkey. A WH_KEYBOARD_LL hook intercepts
// the keystroke earlier, before Explorer sees it, and can swallow it by
// returning a non-zero value from the hook procedure.
type keyboardHook struct {
	handle   windows.Handle
	requests chan<- int
	callback uintptr
}

func newKeyboardHook(requests chan<- int) *keyboardHook {
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
// Win+<digit> combination, hands the desktop index off to the switcher
// goroutine over a non-blocking channel send before swallowing the key.
func (h *keyboardHook) lowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))
		if kb.VkCode >= vk1 && kb.VkCode <= vk9 {
			winDown := isKeyDown(vkLWin) || isKeyDown(vkRWin)
			if winDown && !isKeyDown(vkControl) && !isKeyDown(vkShift) && !isKeyDown(vkMenu) {
				index := int(kb.VkCode - vk1) // 0-based
				select {
				case h.requests <- index:
				default:
					// Switcher is busy; drop the request rather than
					// blocking this hook callback.
				}
				return 1 // swallow the keystroke
			}
		}
	}
	return h.callNext(nCode, wParam, lParam)
}
