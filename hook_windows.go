//go:build windows

package main

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winKeyUpReinjectDelay is how long to wait before reinjecting Win's
// swallowed key-up (see sendKeyUpAfter's doc in winapi_windows.go). Long
// enough that Explorer's own "was a digit key just pressed" window for
// the minimize check has almost certainly lapsed; short enough that
// anything else waiting to see Win released doesn't notice the delay.
const winKeyUpReinjectDelay = 350 * time.Millisecond

// keyboardHook manages the low-level keyboard hook used to intercept
// Win+1..Win+9 (switch to desktop N) and Win+Shift+1..Win+Shift+9 (move
// the foreground window to desktop N) before Explorer's taskbar handles
// them.
//
// RegisterHotKey cannot be used here: Win+<digit> is reserved by the shell
// for launching pinned taskbar apps, and the OS refuses to let normal
// applications register it as a hotkey. A WH_KEYBOARD_LL hook intercepts
// the keystroke earlier, before Explorer sees it, and can swallow it by
// returning a non-zero value from the hook procedure.
type keyboardHook struct {
	handle   windows.Handle
	requests chan<- desktopRequest
	callback uintptr

	// winUsedForCombo tracks whether the current Win key hold has already
	// been used for one of our Win+<digit> combos, so its eventual key-up
	// can also be swallowed. See the note on winKeyUp handling below.
	winUsedForCombo bool
	// winPhysicallyDown distinguishes a genuine fresh Win key-down from
	// one of its auto-repeat key-downs while held, so winUsedForCombo only
	// resets at the start of an actual new press-and-hold cycle.
	winPhysicallyDown bool
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
// swallowing the key.
//
// Both the key-down AND key-up of the digit key are swallowed, which is
// enough to stop Explorer from launching or switching to a pinned taskbar
// app. But when the app at that taskbar position is already the active
// window, Windows instead *minimizes* it on Win+<digit> -- and that
// specific behavior turned out to survive swallowing the digit key
// entirely. It's evidently keyed off the Win key's own key-up (checking
// low-level key state at that point, not off receiving the digit key's
// message), which suppressing the digit key alone can never prevent.
// winUsedForCombo tracks whether Win was used for one of our combos during
// its current hold, so we can swallow the Win key's key-up too in that
// case -- but only that case, so a plain Win tap still opens the Start
// Menu normally.
//
// Swallowing Win's own key-up would otherwise leave anything downstream
// of this hook (Explorer included) thinking Win is still held, since they
// never see it released -- so a synthetic key-up is reinjected via
// SendInput a little later (winKeyUpReinjectDelay), once Explorer's own
// "was a digit key just pressed" window for the minimize check has almost
// certainly lapsed. Reinjecting it immediately was tried first and just
// handed that check the exact event it needed to fire anyway.
func (h *keyboardHook) lowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	isDown := wParam == wmKeyDown || wParam == wmSysKeyDown
	isUp := wParam == wmKeyUp || wParam == wmSysKeyUp

	if int32(nCode) != hcAction || (!isDown && !isUp) {
		return h.callNext(nCode, wParam, lParam)
	}
	kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))

	if kb.VkCode == vkLWin || kb.VkCode == vkRWin {
		if isDown {
			if !h.winPhysicallyDown {
				h.winUsedForCombo = false // genuine fresh press, not an auto-repeat
			}
			h.winPhysicallyDown = true
		} else {
			h.winPhysicallyDown = false
			if h.winUsedForCombo {
				h.winUsedForCombo = false
				sendKeyUpAfter(uint16(kb.VkCode), winKeyUpReinjectDelay)
				return 1 // swallow: see winUsedForCombo doc above
			}
		}
		return h.callNext(nCode, wParam, lParam)
	}

	if hotkeysEnabled.Load() && kb.VkCode >= vk1 && kb.VkCode <= vk9 {
		winDown := isKeyDown(vkLWin) || isKeyDown(vkRWin)
		// Ctrl and Alt are left alone so other Win+Ctrl/Win+Alt
		// combinations keep working; Shift toggles switch vs. move.
		if winDown && !isKeyDown(vkControl) && !isKeyDown(vkMenu) {
			if isDown {
				req := desktopRequest{
					action: actionSwitchToDesktop,
					index:  int(kb.VkCode - vk1), // 0-based
					// Captured here, synchronously, rather than later by
					// the switcher goroutine: see switchTo's doc comment.
					hwndForeground: getForegroundWindow(),
				}
				if isKeyDown(vkShift) {
					req.action = actionMoveWindowToDesktop
				}
				select {
				case h.requests <- req:
				default:
					// Switcher is busy; drop the request rather than
					// blocking this hook callback.
				}
				h.winUsedForCombo = true
			}
			return 1 // swallow both the key-down and key-up
		}
	}
	return h.callNext(nCode, wParam, lParam)
}
