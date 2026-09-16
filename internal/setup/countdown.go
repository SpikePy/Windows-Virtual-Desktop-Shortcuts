package setup

import (
	"fmt"
	"time"
)

// Setup's dialog runs two countdowns: page one installs/updates on its own
// after AutoInstallAfter unless something is clicked, and a successful
// result page closes itself after AutoCloseAfter. The arithmetic lives
// here, free of any OS dependency, so it's tested on any platform.

const (
	AutoInstallAfter = 5 * time.Second
	AutoCloseAfter   = 5 * time.Second
)

// Remaining returns the whole seconds left of total after elapsed,
// rounded up so the display reads 5, 4, ... 1 and never shows 0 while
// time remains, and whether the countdown has run out.
func Remaining(total, elapsed time.Duration) (seconds int, done bool) {
	left := total - elapsed
	if left <= 0 {
		return 0, true
	}
	return int((left + time.Second - 1) / time.Second), false
}

// InstallCountdownText is page one's countdown line.
func InstallCountdownText(seconds int) string {
	return fmt.Sprintf("Installing/updating automatically in %d s...", seconds)
}

// CloseCountdownText is a successful result page's countdown line.
func CloseCountdownText(seconds int) string {
	return fmt.Sprintf("Closing in %d s...", seconds)
}
