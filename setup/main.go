//go:build windows

// Command Setup_VirtualDesktopShortcuts is the combined installer/
// uninstaller for VirtualDesktopShortcuts.exe (see the repo root). Run it
// and it asks whether to install/update or uninstall the tool.
//
// Both actions are safe to run repeatedly and never leave duplicates
// behind:
//   - Install/update always writes to the same fixed path in the current
//     user's Startup folder, skips the write entirely if the downloaded
//     build already matches what's installed (by SHA-256), stops any
//     running instance before replacing the file, and makes sure exactly
//     one instance is running afterwards.
//   - Uninstall stops every running instance and removes the installed
//     file, so nothing is left running or on disk.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	githubOwner   = "SpikePy"
	githubRepo    = "Windows-Virtual-Desktop-Shortcuts"
	targetExeName = "VirtualDesktopShortcuts.exe"
	userAgent     = "Setup_VirtualDesktopShortcuts"
	appTitle      = "Virtual Desktop Shortcuts - Setup"
)

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func main() {
	installFlag := flag.Bool("install", false, "Install or update the tool without prompting")
	uninstallFlag := flag.Bool("uninstall", false, "Uninstall the tool without prompting")
	flag.Parse()

	interactive := !*installFlag && !*uninstallFlag

	action := ""
	switch {
	case *installFlag:
		action = "install"
	case *uninstallFlag:
		action = "uninstall"
	default:
		fmt.Println(appTitle)
		fmt.Println(strings.Repeat("=", len(appTitle)))
		action = promptForAction()
	}

	var err error
	switch action {
	case "install":
		err = installOrUpdate()
	case "uninstall":
		err = uninstall()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
	}

	if interactive {
		waitToClose()
	}

	if err != nil {
		os.Exit(1)
	}
}

// waitToClose closes the window automatically 3 seconds after the action
// is done, or immediately if the user presses Enter first.
func waitToClose() {
	fmt.Println("\nClosing in 3 seconds... (press Enter to close now)")

	enter := make(chan struct{})
	go func() {
		bufio.NewReader(os.Stdin).ReadString('\n')
		close(enter)
	}()

	select {
	case <-enter:
	case <-time.After(3 * time.Second):
	}
}

// promptForAction shows the "install/update or uninstall" menu and reads
// the user's choice from stdin, reprompting on invalid input. If nothing
// is chosen within 5 seconds of the first prompt, it defaults to
// install/update (e.g. for unattended runs).
//
// Input is read by a single background goroutine for the lifetime of the
// prompt, rather than a fresh blocking read per attempt: bufio.Reader
// isn't safe for concurrent reads, and starting a new blocking read on
// every retry (which a naive per-iteration timeout would need) risks two
// reads racing on stdin at once.
func promptForAction() string {
	lines := make(chan string)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- line
		}
	}()

	first := true
	for {
		fmt.Println()
		fmt.Println("What would you like to do?")
		fmt.Println("  1) Install / update")
		fmt.Println("  2) Uninstall")

		var line string
		var ok bool
		if first {
			fmt.Print("Enter choice [1-2] (defaults to Install/update in 5s): ")
			select {
			case line, ok = <-lines:
			case <-time.After(5 * time.Second):
				fmt.Println("\nNo input received -- defaulting to Install / update.")
				return "install"
			}
			first = false
		} else {
			fmt.Print("Enter choice [1-2]: ")
			line, ok = <-lines
		}
		if !ok {
			// stdin closed (e.g. redirected from an empty/closed pipe).
			return "install"
		}

		switch strings.TrimSpace(line) {
		case "1":
			return "install"
		case "2":
			return "uninstall"
		default:
			fmt.Println("Please enter 1 or 2.")
		}
	}
}

func installOrUpdate() error {
	fmt.Println("\nChecking latest release of", githubOwner+"/"+githubRepo, "...")
	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}

	asset := findWindowsAsset(rel)
	if asset == nil {
		return fmt.Errorf("release %s has no windows-amd64 build", rel.TagName)
	}
	fmt.Printf("Latest release: %s (%s)\n", rel.TagName, asset.Name)

	fmt.Println("Downloading", asset.Name, "...")
	exeBytes, err := downloadExe(asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	newHash := sha256.Sum256(exeBytes)

	startupDir, err := startupFolder()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(startupDir, 0o755); err != nil {
		return fmt.Errorf("create startup folder: %w", err)
	}
	destPath := filepath.Join(startupDir, targetExeName)

	if existingHash, ok := hashOfFile(destPath); ok && existingHash == newHash {
		fmt.Println("Already up to date at", destPath)
		ensureRunningOnly(destPath)
		return nil
	}

	fmt.Println("Stopping any running instance...")
	killRunningInstances(targetExeName)

	fmt.Println("Installing to", destPath)
	if err := atomicWriteFile(destPath, exeBytes); err != nil {
		return fmt.Errorf("install %s: %w", destPath, err)
	}

	fmt.Println("Starting", targetExeName, "...")
	if err := startDetached(destPath); err != nil {
		fmt.Fprintln(os.Stderr, "warning: installed but failed to start it:", err)
	}

	fmt.Println("Done. It's installed in Startup and will launch automatically at sign-in.")
	return nil
}

// uninstall stops every running instance of the tool and removes it from
// the Startup folder, leaving nothing installed and nothing running.
func uninstall() error {
	fmt.Println("\nStopping any running instance...")
	killRunningInstances(targetExeName)

	startupDir, err := startupFolder()
	if err != nil {
		return err
	}
	destPath := filepath.Join(startupDir, targetExeName)

	if _, err := os.Stat(destPath); errors.Is(err, os.ErrNotExist) {
		fmt.Println("Not installed (nothing found at", destPath+")")
	} else if err != nil {
		return fmt.Errorf("check %s: %w", destPath, err)
	} else {
		if err := os.Remove(destPath); err != nil {
			return fmt.Errorf("remove %s: %w", destPath, err)
		}
		fmt.Println("Uninstalled:", destPath)
	}
	// Clean up a stray temp file if a previous install/update was interrupted.
	os.Remove(destPath + ".new")
	return nil
}

func fetchLatestRelease() (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	return &rel, nil
}

// findWindowsAsset looks for the exact release asset for the app itself
// (an exact, case-insensitive name match on targetExeName). It's important
// this is an exact match and not a substring/prefix check: the release
// also carries Setup_VirtualDesktopShortcuts.exe, whose name contains
// "VirtualDesktopShortcuts.exe" as a substring too.
func findWindowsAsset(rel *release) *releaseAsset {
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if strings.EqualFold(a.Name, targetExeName) {
			return a
		}
	}
	return nil
}

func downloadExe(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download: %w", err)
	}
	return body, nil
}

func startupFolder() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("%%APPDATA%% environment variable is not set")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), nil
}

func hashOfFile(path string) ([32]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(data), true
}

// atomicWriteFile writes data to dest without ever leaving a partially
// written or duplicate-named file behind: it writes to a sibling temp file
// first, then renames it into place. Rename retries briefly in case the
// old file is still being torn down by a just-terminated process.
func atomicWriteFile(dest string, data []byte) error {
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}

	var lastErr error
	for i := 0; i < 20; i++ {
		if lastErr = os.Rename(tmp, dest); lastErr == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	os.Remove(tmp)
	return lastErr
}

func startDetached(path string) error {
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	return cmd.Start()
}

// killRunningInstances terminates every running process whose image name
// matches exeName (case-insensitive), so an update never runs alongside
// the process it's replacing and a fresh install never ends up with two
// instances racing for the tray icon / hotkey hook.
func killRunningInstances(exeName string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)

	selfPID := windows.GetCurrentProcessId()

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return
	}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if entry.ProcessID != selfPID && strings.EqualFold(name, exeName) {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, entry.ProcessID); err == nil {
				windows.TerminateProcess(h, 0)
				windows.CloseHandle(h)
			}
		}

		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}

	// Give the OS a moment to fully release the file handle of the process
	// image before we try to replace it.
	time.Sleep(200 * time.Millisecond)
}

func isRunning(exeName string) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return false
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), exeName) {
			return true
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			return false
		}
	}
}

func ensureRunningOnly(path string) {
	if isRunning(targetExeName) {
		return
	}
	if err := startDetached(path); err != nil {
		fmt.Fprintln(os.Stderr, "warning: failed to start it:", err)
	}
}
