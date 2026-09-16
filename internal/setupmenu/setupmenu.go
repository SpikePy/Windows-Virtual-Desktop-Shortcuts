// Package setupmenu is the choice and countdown logic behind
// Setup_VirtualDesktopShortcuts.exe's window: Install/update is chosen on
// its own unless the user acts within the countdown, and an unattended
// success closes the window after a short delay. It has no OS dependency,
// so its behaviour can be tested on any platform.
package setupmenu

import (
	"fmt"
	"time"
)

const (
	// Countdown is how long the window waits for a choice before
	// installing/updating on its own, so double-clicking the exe and
	// walking away still gets the tool installed/updated.
	Countdown = 5 * time.Second

	// AutoCloseDelay is how long the window stays open after an
	// unattended success: nobody was there for the countdown, so there's
	// likely nobody left to close it either.
	AutoCloseDelay = 3 * time.Second
)

// Timer counts whole seconds down to zero. The window ticks it once a
// second while nobody has made a choice.
type Timer struct {
	left    int
	stopped bool
}

// NewTimer returns a timer with d left, rounded down to whole seconds.
func NewTimer(d time.Duration) *Timer {
	return &Timer{left: int(d / time.Second)}
}

// Tick uses up one second and reports whether that made the timer run
// out. A stopped or already expired timer never reports it again.
func (t *Timer) Tick() (expired bool) {
	if t.stopped || t.left <= 0 {
		return false
	}
	t.left--
	return t.left == 0
}

// Stop halts the timer for good - the user has shown they're there.
func (t *Timer) Stop() { t.stopped = true }

// Running reports whether the timer is still counting.
func (t *Timer) Running() bool { return !t.stopped && t.left > 0 }

// Label returns text with the seconds left appended while the timer runs,
// e.g. "Install / update (5)".
func (t *Timer) Label(text string) string {
	if !t.Running() {
		return text
	}
	return fmt.Sprintf("%s (%d)", text, t.left)
}

// CloseDelay says when the window should close itself after an action
// finished: after AutoCloseDelay for an unattended success, and never (0)
// otherwise, so an error or a user's own run stays on screen.
func CloseDelay(auto bool, err error) time.Duration {
	if auto && err == nil {
		return AutoCloseDelay
	}
	return 0
}
