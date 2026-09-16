//go:build windows

// Command vds-setup is the single entry point for installing, updating and
// uninstalling VirtualDesktopShortcuts.exe. Run it with no arguments (e.g.
// by double-clicking Setup_VirtualDesktopShortcuts.exe) and it opens a
// small window to choose Install/update or Uninstall - installing/updating
// on its own if nothing is chosen within 5 seconds. Pass -mode to skip the
// window for scripted use; progress then goes to the console it was
// started from.
//
// Built with -ldflags "-H=windowsgui", so double-clicking it never opens a
// console window.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/setup"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/win32"
)

// version is stamped in at build time via -ldflags "-X main.version=...".
var version = "dev"

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

func main() {
	mode := flag.String("mode", "", "skip the window and run this action directly: install or uninstall")
	installDir := flag.String("install-dir", "", "directory to install into/remove from (default: %LOCALAPPDATA%\\VirtualDesktopShortcuts)")
	noLaunch := flag.Bool("no-launch", false, "install/update, but don't start it now (install only)")
	noAutostart := flag.Bool("no-autostart", false, "set autostart: false in config.yaml, so no Startup shortcut is added (install only)")
	keepFiles := flag.Bool("keep-files", false, "remove the Startup shortcut and stop the process, but don't delete the installed files (uninstall only)")
	// Setup no longer uses the GitHub API; the flag is still accepted so
	// scripts written for v0.1.x keep working.
	flag.String("github-token", "", "ignored; kept for compatibility with older scripts")

	// A GUI program has no console of its own: borrow the one it was
	// started from (if any) before flag parsing can print an error.
	attachConsole()
	flag.Parse()

	opts := setup.Options{
		InstallDir:  *installDir,
		NoLaunch:    *noLaunch,
		NoAutostart: *noAutostart,
		KeepFiles:   *keepFiles,
	}

	if *mode == "" {
		// The window shows its own errors; only failing to open it at all
		// needs a message box.
		if err := setup.RunWindow(version, opts); err != nil {
			if errors.Is(err, setup.ErrNoWindow) {
				win32.ErrorBox("Virtual Desktop Shortcuts Setup", err.Error())
			}
			os.Exit(1)
		}
		return
	}

	opts.Progress = func(s string) { fmt.Println(s) }
	var err error
	switch strings.ToLower(*mode) {
	case "install":
		var tag string
		if tag, err = setup.Install(opts); err == nil {
			fmt.Printf("Installed %s.\n", tag)
		}
	case "uninstall":
		if err = setup.Uninstall(opts); err == nil {
			fmt.Println("Uninstalled.")
		}
	default:
		err = fmt.Errorf("unknown -mode %q (want install or uninstall)", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if !hasStdout() {
			win32.ErrorBox("Virtual Desktop Shortcuts Setup", err.Error())
		}
		os.Exit(1)
	}
}

// attachConsole points stdout and stderr at the parent's console when the
// program was started from one without redirected output. When output is
// already redirected (a pipe or file, e.g. from a script), the inherited
// handles are used as they are.
func attachConsole() {
	if hasStdout() {
		return
	}
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS
	if r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); r == 0 {
		return // started without a console, e.g. from Explorer
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
		fmt.Println() // start below the prompt the shell has already printed
	}
}

// hasStdout reports whether the process has a usable standard output.
func hasStdout() bool {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	return err == nil && h != 0 && h != windows.InvalidHandle
}
