//go:build windows

// Command vds-setup is the single entry point for installing, updating and
// uninstalling VirtualDesktopShortcuts.exe. Run it with no arguments (e.g.
// by double-clicking Setup_VirtualDesktopShortcuts.exe) and it shows an
// interactive menu to choose "Install / update" or "Uninstall" -
// defaulting to "Install / update" on its own if nothing is chosen within
// promptTimeout. Pass -mode to skip the prompt for scripted use.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/setup"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/setupmenu"
)

// version is stamped in at build time via -ldflags "-X main.version=...".
var version = "dev"

// promptTimeout is how long the menu waits for a first keypress before
// defaulting to "install" on its own - so double-clicking the exe and
// walking away still gets the tool installed/updated.
const promptTimeout = 5 * time.Second

// autoExitTimeout caps the final "press Enter to exit" wait when the
// action was auto-chosen and succeeded: nobody was at the keyboard for
// promptTimeout, so there's likely nobody left to press Enter either. A
// failed auto-chosen run waits for Enter like a manual one, so the error
// is still on screen for whoever comes back to it.
const autoExitTimeout = 3 * time.Second

func main() {
	mode := flag.String("mode", "", "skip the interactive menu and run this action directly: install or uninstall")
	installDir := flag.String("install-dir", "", "directory to install into/remove from (default: %LOCALAPPDATA%\\VirtualDesktopShortcuts)")
	githubToken := flag.String("github-token", "", "optional GitHub token, to avoid the unauthenticated API rate limit (install only)")
	noLaunch := flag.Bool("no-launch", false, "install/update and add the Startup shortcut, but don't start it now (install only)")
	noAutostart := flag.Bool("no-autostart", false, "don't create (or refresh) the Startup shortcut (install only)")
	keepFiles := flag.Bool("keep-files", false, "remove the Startup shortcut and stop the process, but don't delete the installed files (uninstall only)")
	flag.Parse()

	interactive := *mode == ""
	action := strings.ToLower(*mode)
	autoChosen := false

	var in setupmenu.Lines
	if interactive {
		fmt.Printf("Virtual Desktop Shortcuts setup %s\n\n", version)
		in = setupmenu.ReadLines(os.Stdin)
		var err error
		action, autoChosen, err = setupmenu.Prompt(in, os.Stdout, promptTimeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}

	var err error
	switch action {
	case "install":
		err = setup.Install(setup.InstallOptions{
			InstallDir:  *installDir,
			GitHubToken: *githubToken,
			NoLaunch:    *noLaunch,
			NoAutostart: *noAutostart,
		})
	case "uninstall":
		err = setup.Uninstall(setup.UninstallOptions{
			InstallDir: *installDir,
			KeepFiles:  *keepFiles,
		})
	default:
		fmt.Fprintf(os.Stderr, "error: unknown -mode %q (want install or uninstall)\n", *mode)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	if interactive {
		var exitAfter time.Duration
		if autoChosen && err == nil {
			exitAfter = autoExitTimeout
		}
		setupmenu.WaitForEnter(in, os.Stdout, exitAfter)
	}
	if err != nil {
		os.Exit(1)
	}
}
