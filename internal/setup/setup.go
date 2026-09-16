//go:build windows

// Package setup implements the install and uninstall actions behind
// Setup_VirtualDesktopShortcuts.exe (whose dialog lives in cmd/vds-setup):
// downloading VirtualDesktopShortcuts.exe into the user's own
// %LOCALAPPDATA% and adding the autostart shortcut if config.yaml asks for
// it, and reversing that - removing the shortcut, stopping any running
// copy, and deleting the installed files.
//
// Everything here is per-user, so none of it needs administrator rights.
package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/config"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/shortcut"
)

const (
	// assetName is the release asset and the installed exe's name.
	assetName = "VirtualDesktopShortcuts.exe"

	userAgent = "Setup_VirtualDesktopShortcuts"
)

// installDirName is the per-user directory the exe and its config.yaml
// share, under %LOCALAPPDATA%.
const installDirName = "VirtualDesktopShortcuts"

// resolveInstallDir returns dir, or %LOCALAPPDATA%\VirtualDesktopShortcuts
// if dir is empty.
func resolveInstallDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", fmt.Errorf("%%LOCALAPPDATA%% is not set")
	}
	return filepath.Join(base, installDirName), nil
}

// Options configures Install and Uninstall.
type Options struct {
	InstallDir  string       // defaults to %LOCALAPPDATA%\VirtualDesktopShortcuts if empty
	NoLaunch    bool         // install/update without starting it now, even with autostart on
	NoAutostart bool         // install: turn autostart off in config.yaml instead of following it
	KeepFiles   bool         // uninstall: remove autostart and stop the process, but leave the files
	Progress    func(string) // told about each step; may be nil
}

func (o Options) progress(format string, args ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, args...))
	}
}

// Install downloads the latest released VirtualDesktopShortcuts.exe,
// installs it under the current user's %LOCALAPPDATA%, adds or removes the
// Startup shortcut as config.yaml's autostart setting says, and - only
// while autostart is on - (re)starts it, terminating any already-running
// copy first so the file can be replaced and so at most one copy is ever
// running. Safe to re-run to
// update in place: it always ends up with at most one shortcut (the same
// fixed name) and one running instance (the app itself also refuses to
// start a second copy via a named mutex - see internal/singleinstance - so
// this is belt and suspenders). It returns the installed release's tag and
// whether the app was started.
func Install(opts Options) (tag string, started bool, err error) {
	installDir, err := resolveInstallDir(opts.InstallDir)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", false, fmt.Errorf("creating install dir: %w", err)
	}
	targetPath := filepath.Join(installDir, assetName)

	opts.progress("Looking up the latest release...")
	tag, err = latestTag()
	if err != nil {
		return "", false, fmt.Errorf("looking up the latest release: %w", err)
	}

	opts.progress("Downloading %s...", tag)
	tmpPath := targetPath + ".download"
	if err := downloadFile(downloadURL(assetName), tmpPath); err != nil {
		return "", false, fmt.Errorf("downloading %s: %w", assetName, err)
	}

	opts.progress("Stopping the running app...")
	if err := terminateRunning(assetName); err != nil {
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("stopping the running app: %w", err)
	}

	opts.progress("Installing to %s...", targetPath)
	if err := replaceFile(tmpPath, targetPath); err != nil {
		return "", false, fmt.Errorf("installing: %w", err)
	}

	if opts.NoAutostart {
		if err := config.SetAutostart(false); err != nil {
			return "", false, fmt.Errorf("turning autostart off in config.yaml: %w", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return "", false, fmt.Errorf("reading config.yaml: %w", err)
	}
	if cfg.Autostart {
		opts.progress("Adding it to the Startup folder...")
	} else {
		opts.progress("Autostart is off in config.yaml - leaving it out of the Startup folder...")
	}
	if err := shortcut.Autostart(cfg.Autostart, targetPath); err != nil {
		return "", false, fmt.Errorf("updating autostart: %w", err)
	}

	if !cfg.Autostart || opts.NoLaunch {
		return tag, false, nil
	}
	opts.progress("Starting it...")
	cmd := exec.Command(targetPath)
	cmd.Dir = installDir
	if err := cmd.Start(); err != nil {
		return "", false, fmt.Errorf("starting %s: %w", targetPath, err)
	}
	return tag, true, nil
}

// Uninstall reverses Install: removes the Startup shortcut, terminates any
// running copy, and (unless KeepFiles) deletes the installed files. The
// user's config.yaml lives in the same directory and goes with it.
func Uninstall(opts Options) error {
	installDir, err := resolveInstallDir(opts.InstallDir)
	if err != nil {
		return err
	}

	opts.progress("Removing it from the Startup folder...")
	if err := shortcut.Autostart(false, ""); err != nil {
		return fmt.Errorf("removing autostart: %w", err)
	}

	opts.progress("Stopping the running app...")
	if err := terminateRunning(assetName); err != nil {
		return fmt.Errorf("stopping %s: %w", assetName, err)
	}

	if !opts.KeepFiles {
		opts.progress("Removing %s...", installDir)
		if err := os.RemoveAll(installDir); err != nil {
			return fmt.Errorf("removing %s: %w", installDir, err)
		}
	}
	return nil
}

// downloadFile saves url's content to destPath, removing the file again
// if the download fails partway.
func downloadFile(url, destPath string) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if err := httpGet(url, out); err != nil {
		out.Close()
		os.Remove(destPath)
		return err
	}
	return out.Close()
}

// replaceFile moves tmpPath onto targetPath, retrying briefly: the target
// may still be momentarily locked right after terminateRunning killed the
// process that had it open/mapped.
func replaceFile(tmpPath, targetPath string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(tmpPath, targetPath); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	os.Remove(tmpPath)
	return err
}

// terminateRunning finds every running process whose image file name
// matches exeName (case-insensitively) and terminates it, waiting briefly
// for each to actually exit.
func terminateRunning(exeName string) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	self := windows.GetCurrentProcessId()
	var pids []uint32
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID != self && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), exeName) {
			pids = append(pids, entry.ProcessID)
		}
	}

	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
		if err != nil {
			continue // already gone, or no permission - nothing more we can do
		}
		windows.TerminateProcess(h, 0)
		windows.WaitForSingleObject(h, 5000)
		windows.CloseHandle(h)
	}
	return nil
}
