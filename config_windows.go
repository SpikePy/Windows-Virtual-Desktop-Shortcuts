//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// hotkeysEnabled gates whether the keyboard hook intercepts Win+1..9. It's
// the single source of truth shared between the tray menu's Enable/Disable
// items and the config file's `enabled` setting; both read and write it,
// and the hook checks it on every keystroke.
var hotkeysEnabled atomic.Bool

type config struct {
	Enabled bool
}

const defaultConfigTemplate = `# VirtualDesktopShortcuts configuration
#
# Edit and save this file to change how the app behaves. Changes are
# picked up automatically within a couple of seconds -- no restart needed.

# Turn the Win+1..9 shortcuts on or off without uninstalling the app.
# You can also toggle this from the tray icon's right-click menu.
enabled: true
`

func configDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("%%APPDATA%% environment variable is not set")
	}
	return filepath.Join(appData, "VirtualDesktopShortcuts"), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ensureConfigFile creates the config file with default contents if it
// doesn't exist yet, and returns its path either way.
func ensureConfigFile() (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("create config folder: %w", err)
		}
		if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o644); err != nil {
			return "", fmt.Errorf("write default config: %w", err)
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
}

// loadConfig reads and parses the config file with a minimal, tolerant
// "key: value" line parser (adequate for this file's handful of flat
// settings, without pulling in a YAML dependency). A missing file, missing
// key, or unparseable line all fall back to defaults rather than erroring.
func loadConfig() config {
	cfg := config{Enabled: true}

	path, err := configPath()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if idx := strings.IndexByte(value, '#'); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		value = strings.Trim(value, `"'`)

		switch key {
		case "enabled":
			cfg.Enabled = value == "true"
		}
	}
	return cfg
}

var enabledLineRE = regexp.MustCompile(`(?m)^([ \t]*enabled[ \t]*:[ \t]*)\S+(.*)$`)

// setEnabledInFile updates the `enabled` value in place, preserving the
// rest of the file (comments included) so a user's own edits survive a
// tray-menu toggle. If the key isn't present, it's appended.
func setEnabledInFile(enabled bool) error {
	path, err := ensureConfigFile()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	val := "false"
	if enabled {
		val = "true"
	}

	var out []byte
	if enabledLineRE.Match(data) {
		out = enabledLineRE.ReplaceAll(data, []byte("${1}"+val+"$2"))
	} else {
		out = data
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, []byte("enabled: "+val+"\n")...)
	}

	return os.WriteFile(path, out, 0o644)
}

// startConfigWatcher loads the config synchronously (so hotkeysEnabled has
// its correct initial value before the keyboard hook installs), then
// polls the file's mtime in the background so external edits -- made in
// whatever editor "Configure" opened -- take effect within a couple of
// seconds without needing a restart.
func startConfigWatcher() {
	hotkeysEnabled.Store(loadConfig().Enabled)

	go func() {
		var lastMod time.Time
		if path, err := configPath(); err == nil {
			if fi, err := os.Stat(path); err == nil {
				lastMod = fi.ModTime()
			}
		}
		for {
			time.Sleep(2 * time.Second)

			path, err := configPath()
			if err != nil {
				continue
			}
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			if fi.ModTime().Equal(lastMod) {
				continue
			}
			lastMod = fi.ModTime()
			hotkeysEnabled.Store(loadConfig().Enabled)
		}
	}()
}
