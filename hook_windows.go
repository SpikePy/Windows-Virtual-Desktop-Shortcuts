//go:build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// keyboardHook manages the low-level keyboard hook used to intercept
// Win+1..Win+9 (switch to desktop N), Win+Left/Right (switch to the
// previous/next desktop), and the same with Shift (move the foreground
// window there instead).
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

	// winDownSeen tracks the Win key's last logged state, so only its
	// press and release are logged and not its auto-repeat.
	winDownSeen bool
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

func winKeyHeld() bool {
	return isKeyDown(vkLWin) || isKeyDown(vkRWin)
}

// llkhfInjected is KBDLLHOOKSTRUCT's LLKHF_INJECTED flag: the event came
// from SendInput rather than the keyboard.
const llkhfInjected = 0x10

func keyDirection(down bool) string {
	if down {
		return "down"
	}
	return "up"
}

// lowLevelKeyboardProc is the WH_KEYBOARD_LL hook procedure. It must return
// quickly: it only classifies the keystroke and, if it is one of the
// shortcuts (see shortcutFor), hands the request off to the switcher
// goroutine over a non-blocking channel send before swallowing both the
// key's key-down and key-up.
func (h *keyboardHook) lowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	isDown := wParam == wmKeyDown || wParam == wmSysKeyDown
	isUp := wParam == wmKeyUp || wParam == wmSysKeyUp

	if int32(nCode) != hcAction || (!isDown && !isUp) {
		return h.callNext(nCode, wParam, lParam)
	}
	kb := (*kbdllhookstruct)(unsafe.Pointer(lParam))

	start := time.Now()
	defer func() {
		if d := time.Since(start); d > hookSlowThreshold {
			hookLogf("hook: handling key 0x%X took %v", kb.VkCode, d.Round(time.Millisecond))
		}
	}()

	if (kb.VkCode == vkLWin || kb.VkCode == vkRWin) && isDown != h.winDownSeen {
		h.winDownSeen = isDown
		hookLogf("hook: Win %s", keyDirection(isDown))
	}
	if kb.VkCode >= vk1 && kb.VkCode <= vk9 {
		hookLogf("hook: key %d %s (Win held: %v, Ctrl: %v, Alt: %v, Shift: %v, injected: %v)",
			kb.VkCode-vk1+1, keyDirection(isDown), winKeyHeld(), isKeyDown(vkControl),
			isKeyDown(vkMenu), isKeyDown(vkShift), kb.Flags&llkhfInjected != 0)
	}
	if !hotkeysEnabled.Load() {
		return h.callNext(nCode, wParam, lParam)
	}
	req, ok := shortcutFor(kb.VkCode)
	if !ok {
		return h.callNext(nCode, wParam, lParam)
	}
	// Ctrl and Alt are left alone so other Win+Ctrl/Win+Alt combinations,
	// like Windows' own Win+Ctrl+Left/Right desktop switching, keep
	// working; Shift toggles switch vs. move.
	if !winKeyHeld() || isKeyDown(vkControl) || isKeyDown(vkMenu) {
		return h.callNext(nCode, wParam, lParam)
	}

	if isDown {
		if isKeyDown(vkShift) {
			req.action = actionMoveWindowToDesktop
		}
		hookLogf("hook: Win+%s (action %d)", keyName(kb.VkCode), req.action)
		select {
		case h.requests <- req:
		default:
			// Switcher is busy; drop the request rather than blocking
			// this hook callback.
			hookLogf("hook: switcher busy, dropped Win+%s", keyName(kb.VkCode))
		}
	}
	return 1
}

// hookSlowThreshold is how long a hook call may take before it's logged.
// If the hook procedure runs past LowLevelHooksTimeout, Windows ignores
// its result and passes the key on anyway (and eventually removes the
// hook), so slow calls are worth knowing about.
const hookSlowThreshold = 20 * time.Millisecond

// hookLogs carries log lines from the hook procedure to a goroutine that
// writes them, so the hook never waits on OutputDebugString, which blocks
// until a debugger such as DebugView has taken each line.
var hookLogs = make(chan string, 64)

func init() {
	go func() {
		for line := range hookLogs {
			debugLogf("%s", line)
		}
	}()
}

// hookLogf queues a log line from the hook procedure, dropping it if the
// queue is full rather than blocking.
func hookLogf(format string, args ...any) {
	select {
	case hookLogs <- fmt.Sprintf(format, args...):
	default:
	}
}

// shortcutFor maps a key pressed together with Win to the desktop it
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

func keyName(vk uint32) string {
	switch vk {
	case vkLeft:
		return "Left"
	case vkRight:
		return "Right"
	}
	return string(rune(vk))
}
