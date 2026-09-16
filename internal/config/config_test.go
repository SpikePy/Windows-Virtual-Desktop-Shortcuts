package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useTempDir points Load, Path and SetEnabled at a fresh directory and
// returns the config.yaml path they will use.
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	return filepath.Join(dir, dirName, fileName)
}

func TestLoadCreatesDefaultFile(t *testing.T) {
	path := useTempDir(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != defaults() {
		t.Errorf("first Load = %+v, want defaults %+v", cfg, defaults())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("default config.yaml was not created: %v", err)
	}
	for _, want := range []string{"enabled: true", "autostart: true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("created config.yaml is missing %q:\n%s", want, data)
		}
	}

	again, err := Load()
	if err != nil || again != defaults() {
		t.Errorf("reloading the generated file = %+v, %v; want defaults, nil", again, err)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want Config
	}{
		{"enabled true", "enabled: true\n", Config{Enabled: true, Autostart: true}},
		{"enabled false", "enabled: false\n", Config{Enabled: false, Autostart: true}},
		{"autostart false", "autostart: false\n", Config{Enabled: true, Autostart: false}},
		{"both set", "enabled: false\nautostart: false\n", Config{Enabled: false, Autostart: false}},
		{"indented and spaced", "  enabled:   false  \n", Config{Enabled: false, Autostart: true}},
		{"trailing comment", "enabled: false # off for now\n", Config{Enabled: false, Autostart: true}},
		{"quoted value", `autostart: "false"` + "\n", Config{Enabled: true, Autostart: false}},
		{"comments and blank lines only", "# nothing here\n\n", defaults()},
		{"unknown keys are ignored", "shortcut: ctrl+alt\nenabled: false\n", Config{Enabled: false, Autostart: true}},
		{"unreadable value keeps the default", "enabled: maybe\nautostart: 1\n", defaults()},
		{"missing key keeps the default", "other: 1\n", defaults()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := useTempDir(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg != tt.want {
				t.Errorf("Load = %+v, want %+v", cfg, tt.want)
			}
		})
	}
}

func TestSetEnabledPreservesTheRestOfTheFile(t *testing.T) {
	path := useTempDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "# my notes\nenabled: true\n# trailing comment\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "enabled: false") {
		t.Errorf("SetEnabled(false) did not update the value:\n%s", got)
	}
	for _, keep := range []string{"# my notes", "# trailing comment"} {
		if !strings.Contains(got, keep) {
			t.Errorf("SetEnabled dropped %q:\n%s", keep, got)
		}
	}

	cfg, err := Load()
	if err != nil || cfg.Enabled {
		t.Errorf("Load after SetEnabled(false) = %+v, %v; want Enabled false", cfg, err)
	}
}

func TestSetEnabledAppendsAMissingKey(t *testing.T) {
	path := useTempDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# only a comment"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.Enabled {
		t.Errorf("Load = %+v, %v; want Enabled false", cfg, err)
	}
}

func TestSetAutostartLeavesEnabledAlone(t *testing.T) {
	useTempDir(t)
	if err := SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := SetAutostart(false); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg != (Config{Enabled: false, Autostart: false}) {
		t.Errorf("Load = %+v, %v; want both false", cfg, err)
	}
	if err := SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil || cfg != (Config{Enabled: false, Autostart: true}) {
		t.Errorf("Load = %+v, %v; want Enabled false, Autostart true", cfg, err)
	}
}

func TestSetInAppendsOnlyWhenMissing(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"replaces in place", "a: 1\nautostart: true # note\n", "a: 1\nautostart: false # note\n"},
		{"appends with a newline", "# x", "# x\nautostart: false\n"},
		{"appends to empty", "", "autostart: false\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(setIn([]byte(tt.in), "autostart", false)); got != tt.want {
				t.Errorf("setIn = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureCreatesTheFileOnce(t *testing.T) {
	path := useTempDir(t)

	got, err := Ensure()
	if err != nil || got != path {
		t.Fatalf("Ensure() = %q, %v; want %q, nil", got, err, path)
	}
	if err := os.WriteFile(path, []byte("enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "enabled: true") {
		t.Error("Ensure overwrote an existing config.yaml")
	}
}

func TestWatchReportsAChange(t *testing.T) {
	path := useTempDir(t)
	if _, err := Ensure(); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	changes := make(chan Config, 1)
	go Watch(stop, 10*time.Millisecond, func(c Config) { changes <- c })

	if err := os.WriteFile(path, []byte("enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Watch reads the file's timestamp once before its first tick, and
	// that read can land either side of the write above. Rather than
	// depend on which, keep moving the timestamp forward until Watch
	// notices: whenever it read last, the next bump is a change to it.
	// Setting it explicitly also copes with filesystems that store only
	// whole-second timestamps.
	bump := time.NewTicker(25 * time.Millisecond)
	defer bump.Stop()
	deadline := time.After(5 * time.Second)
	for step := 1; ; step++ {
		select {
		case cfg := <-changes:
			if cfg.Enabled {
				t.Errorf("Watch reported %+v, want Enabled false", cfg)
			}
			return
		case <-deadline:
			t.Fatal("Watch did not report the change")
		case <-bump.C:
			ts := time.Now().Add(time.Duration(step) * time.Second)
			if err := os.Chtimes(path, ts, ts); err != nil {
				t.Fatal(err)
			}
		}
	}
}
