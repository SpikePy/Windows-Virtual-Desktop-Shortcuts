package setup

import (
	"testing"
	"time"
)

func TestRemaining(t *testing.T) {
	const total = 5 * time.Second
	tests := []struct {
		elapsed time.Duration
		seconds int
		done    bool
	}{
		{0, 5, false},
		{200 * time.Millisecond, 5, false},
		{999 * time.Millisecond, 5, false},
		{time.Second, 4, false},
		{1200 * time.Millisecond, 4, false},
		{4 * time.Second, 1, false},
		{4999 * time.Millisecond, 1, false},
		{5 * time.Second, 0, true},
		{7 * time.Second, 0, true},
	}
	for _, tt := range tests {
		seconds, done := Remaining(total, tt.elapsed)
		if seconds != tt.seconds || done != tt.done {
			t.Errorf("Remaining(%v, %v) = %d, %t; want %d, %t", total, tt.elapsed, seconds, done, tt.seconds, tt.done)
		}
	}
}

func TestCountdownTexts(t *testing.T) {
	if got, want := InstallCountdownText(5), "Installing/updating automatically in 5 s..."; got != want {
		t.Errorf("InstallCountdownText = %q, want %q", got, want)
	}
	if got, want := CloseCountdownText(3), "Closing in 3 s..."; got != want {
		t.Errorf("CloseCountdownText = %q, want %q", got, want)
	}
}
