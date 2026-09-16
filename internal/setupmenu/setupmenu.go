// Package setupmenu is Setup_VirtualDesktopShortcuts.exe's interactive
// console menu. It has no OS dependency, so its timing behaviour can be
// tested on any platform.
package setupmenu

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

// Lines delivers input line by line from one background reader that lives
// for the whole process, so Prompt and WaitForEnter share it instead of
// racing two separate reads against the same console input.
type Lines struct {
	lines <-chan string
	errs  <-chan error
}

// ReadLines starts reading r line by line in the background.
func ReadLines(r io.Reader) Lines {
	lines := make(chan string)
	errs := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(r)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				errs <- err
				return
			}
			lines <- line
		}
	}()
	return Lines{lines: lines, errs: errs}
}

// Prompt writes the menu to w and returns "install" or "uninstall", plus
// whether it was auto-chosen. If nothing is chosen within countdown of the
// first prompt, it returns "install" on its own; once the user has typed
// anything (even an invalid choice), later reprompts wait indefinitely -
// they've shown they're there.
func Prompt(in Lines, w io.Writer, countdown time.Duration) (action string, auto bool, err error) {
	for {
		fmt.Fprintln(w, "Virtual Desktop Shortcuts - Setup")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  1) Install / update")
		fmt.Fprintln(w, "  2) Uninstall")
		fmt.Fprintln(w)

		var expired <-chan time.Time // stays nil (never fires) once the countdown is used up
		if countdown > 0 {
			fmt.Fprintf(w, "Choose an option [1-2] (installing/updating automatically in %d seconds if nothing is chosen): ", int(countdown.Seconds()))
			expired = time.After(countdown)
			countdown = 0
		} else {
			fmt.Fprint(w, "Choose an option [1-2]: ")
		}

		var line string
		select {
		case line = <-in.lines:
		case e := <-in.errs:
			return "", false, fmt.Errorf("reading input: %w", e)
		case <-expired:
			fmt.Fprintln(w)
			fmt.Fprintln(w, "No input received - installing/updating automatically.")
			return "install", true, nil
		}

		switch strings.TrimSpace(line) {
		case "1":
			return "install", false, nil
		case "2":
			return "uninstall", false, nil
		default:
			fmt.Fprintln(w, "Please enter 1 or 2.")
			fmt.Fprintln(w)
		}
	}
}

// WaitForEnter keeps the console window open (double-clicking the exe
// opens one that would otherwise close immediately on exit) until the
// user presses Enter - or, if exitAfter > 0, until that much time has
// passed.
func WaitForEnter(in Lines, w io.Writer, exitAfter time.Duration) {
	fmt.Fprintln(w)
	var expired <-chan time.Time // nil (never fires) unless exitAfter > 0
	if exitAfter > 0 {
		fmt.Fprintf(w, "Exiting automatically in %d seconds (press Enter to exit now)...", int(exitAfter.Seconds()))
		expired = time.After(exitAfter)
	} else {
		fmt.Fprint(w, "Press Enter to exit...")
	}
	select {
	case <-in.lines:
	case <-in.errs:
	case <-expired:
	}
	if exitAfter > 0 {
		fmt.Fprintln(w)
	}
}
