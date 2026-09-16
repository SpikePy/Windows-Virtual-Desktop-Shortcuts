package setupmenu

import (
	"errors"
	"testing"
	"time"
)

func TestTimerCountsDownToExpiry(t *testing.T) {
	tm := NewTimer(3 * time.Second)
	wantLabels := []string{"Go (3)", "Go (2)", "Go (1)"}
	for i, want := range wantLabels {
		if got := tm.Label("Go"); got != want {
			t.Errorf("before tick %d: Label = %q, want %q", i, got, want)
		}
		expired := tm.Tick()
		if last := i == len(wantLabels)-1; expired != last {
			t.Errorf("tick %d: expired = %t, want %t", i, expired, last)
		}
	}
	if tm.Running() {
		t.Error("timer still running after it expired")
	}
	if got := tm.Label("Go"); got != "Go" {
		t.Errorf("expired Label = %q, want %q", got, "Go")
	}
	if tm.Tick() {
		t.Error("an expired timer reported expiry again")
	}
}

func TestStoppedTimerNeverExpires(t *testing.T) {
	tm := NewTimer(2 * time.Second)
	tm.Tick()
	tm.Stop()
	for i := 0; i < 5; i++ {
		if tm.Tick() {
			t.Fatalf("stopped timer expired on tick %d", i)
		}
	}
	if tm.Running() || tm.Label("Go") != "Go" {
		t.Errorf("stopped timer: Running %t, Label %q", tm.Running(), tm.Label("Go"))
	}
}

func TestCountdownIsFiveSeconds(t *testing.T) {
	tm := NewTimer(Countdown)
	if got := tm.Label("Install / update"); got != "Install / update (5)" {
		t.Errorf("Label = %q", got)
	}
}

func TestCloseDelay(t *testing.T) {
	tests := []struct {
		name string
		auto bool
		err  error
		want time.Duration
	}{
		{"unattended success closes itself", true, nil, AutoCloseDelay},
		{"unattended failure stays open", true, errors.New("boom"), 0},
		{"chosen success stays open", false, nil, 0},
		{"chosen failure stays open", false, errors.New("boom"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CloseDelay(tt.auto, tt.err); got != tt.want {
				t.Errorf("CloseDelay = %v, want %v", got, tt.want)
			}
		})
	}
}
