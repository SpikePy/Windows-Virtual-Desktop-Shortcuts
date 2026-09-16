package setupmenu

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPromptChoices(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"1 installs", "1\n", "install"},
		{"2 uninstalls", "2\n", "uninstall"},
		{"surrounding spaces are ignored", "  2  \n", "uninstall"},
		{"reprompts until a valid choice", "x\n\n1\n", "install"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got, auto, err := Prompt(ReadLines(strings.NewReader(tt.input)), &out, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Prompt = %q, want %q", got, tt.want)
			}
			if auto {
				t.Error("Prompt reported the choice as automatic")
			}
			if !strings.Contains(out.String(), "Install / update") {
				t.Errorf("menu was not printed:\n%s", out.String())
			}
		})
	}
}

func TestPromptInstallsWhenNothingIsChosen(t *testing.T) {
	var out bytes.Buffer
	start := time.Now()
	got, auto, err := Prompt(ReadLines(emptyBlockingReader{}), &out, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != "install" || !auto {
		t.Errorf("Prompt = %q, auto %v; want \"install\", true", got, auto)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("Prompt returned after %v, want at least the countdown", elapsed)
	}
	if !strings.Contains(out.String(), "No input received") {
		t.Errorf("no countdown notice printed:\n%s", out.String())
	}
}

// After an invalid entry the user has shown they're there, so the
// countdown must not fire behind their back.
func TestPromptStopsCountingDownOnceTheUserTypes(t *testing.T) {
	var out bytes.Buffer
	got, auto, err := Prompt(ReadLines(strings.NewReader("x\n2\n")), &out, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != "uninstall" || auto {
		t.Errorf("Prompt = %q, auto %v; want \"uninstall\", false", got, auto)
	}
}

func TestPromptReportsAClosedInput(t *testing.T) {
	var out bytes.Buffer
	if _, _, err := Prompt(ReadLines(strings.NewReader("")), &out, 0); err == nil {
		t.Error("Prompt returned no error for closed input")
	}
}

func TestWaitForEnterReturnsOnEnter(t *testing.T) {
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		WaitForEnter(ReadLines(strings.NewReader("\n")), &out, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForEnter did not return after Enter")
	}
	if !strings.Contains(out.String(), "Press Enter to exit") {
		t.Errorf("no prompt printed:\n%s", out.String())
	}
}

func TestWaitForEnterReturnsOnTimeout(t *testing.T) {
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		WaitForEnter(ReadLines(emptyBlockingReader{}), &out, 20*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForEnter did not return after its timeout")
	}
	if !strings.Contains(out.String(), "Exiting automatically") {
		t.Errorf("no countdown printed:\n%s", out.String())
	}
}

// emptyBlockingReader never returns data or an error, standing in for a
// console nobody is typing at.
type emptyBlockingReader struct{}

func (emptyBlockingReader) Read([]byte) (int, error) { select {} }
