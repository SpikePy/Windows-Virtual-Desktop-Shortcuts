// Package applog provides the opt-in diagnostics log this program appends
// to next to its exe. Write failures are silently ignored: logging must
// never be the reason the program misbehaves.
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// New returns a printf-style logger that appends timestamped lines to
// fileName in the running exe's directory, plus that file's path (empty
// if the exe's location can't be determined). If enabled is false, or the
// path is empty, the logger does nothing.
func New(fileName string, enabled bool) (logf func(format string, args ...any), path string) {
	if exe, err := os.Executable(); err == nil {
		path = filepath.Join(filepath.Dir(exe), fileName)
	}
	logf = func(format string, args ...any) {
		if !enabled || path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
	}
	return logf, path
}
