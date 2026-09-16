//go:build windows

// Package setup implements the install and uninstall actions shared by
// Setup_VirtualDesktopShortcuts.exe: downloading VirtualDesktopShortcuts.exe
// into the user's own %LOCALAPPDATA% and registering it for autostart, and
// reversing that - removing the autostart shortcut, stopping any running
// copy, and deleting the installed files.
//
// Everything here is per-user, so none of it needs administrator rights.
package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	repoOwner = "SpikePy"
	repoName  = "Windows-Virtual-Desktop-Shortcuts"

	// assetName is the release asset and the installed exe's name.
	assetName = "VirtualDesktopShortcuts.exe"

	// startupLinkName is the shortcut dropped in the user's own Startup
	// folder. Versions up to v0.0.27 installed the exe itself there
	// instead; legacyStartupExe exists only to clean that up.
	startupLinkName  = "VirtualDesktopShortcuts.lnk"
	legacyStartupExe = "VirtualDesktopShortcuts.exe"

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

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// InstallOptions configures Install.
type InstallOptions struct {
	InstallDir  string // defaults to %LOCALAPPDATA%\VirtualDesktopShortcuts if empty
	GitHubToken string // optional, avoids the unauthenticated API rate limit
	NoLaunch    bool   // install/update without starting it now
	NoAutostart bool   // don't create (or refresh) the Startup shortcut
}

// Install downloads the latest released VirtualDesktopShortcuts.exe,
// installs it under the current user's %LOCALAPPDATA%, adds a shortcut to
// the Startup folder, and (re)starts it - terminating any already-running
// copy first so the file can be replaced and so at most one copy is ever
// running. Safe to re-run to update in place: it always ends up with
// exactly one shortcut (the same fixed name) and one running instance (the
// app itself also refuses to start a second copy via a named mutex - see
// internal/singleinstance - so this is belt and suspenders).
func Install(opts InstallOptions) error {
	installDir, err := resolveInstallDir(opts.InstallDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("creating install dir: %w", err)
	}
	targetPath := filepath.Join(installDir, assetName)

	fmt.Printf("Looking up latest release of %s/%s...\n", repoOwner, repoName)
	rel, err := latestRelease(opts.GitHubToken)
	if err != nil {
		return fmt.Errorf("fetching latest release: %w", err)
	}
	var downloadURL string
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, assetName) {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("release %s has no asset named %s", rel.TagName, assetName)
	}
	fmt.Printf("Downloading %s (%s)...\n", rel.TagName, downloadURL)

	tmpPath := targetPath + ".download"
	if err := downloadFile(downloadURL, tmpPath); err != nil {
		return fmt.Errorf("downloading asset: %w", err)
	}

	fmt.Println("Stopping any already-running instance...")
	if err := terminateRunning(assetName); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("stopping running instance: %w", err)
	}

	fmt.Printf("Installing to %s...\n", targetPath)
	if err := replaceFile(tmpPath, targetPath); err != nil {
		return fmt.Errorf("installing: %w", err)
	}

	if !opts.NoAutostart {
		fmt.Println("Adding it to the Startup folder...")
		if err := setAutostart(targetPath); err != nil {
			return fmt.Errorf("registering autostart: %w", err)
		}
	}

	if !opts.NoLaunch {
		fmt.Println("Starting it now...")
		cmd := exec.Command(targetPath)
		cmd.Dir = installDir
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("starting %s: %w", targetPath, err)
		}
	}

	fmt.Println("Done.")
	return nil
}

// UninstallOptions configures Uninstall.
type UninstallOptions struct {
	InstallDir string // defaults to %LOCALAPPDATA%\VirtualDesktopShortcuts if empty
	KeepFiles  bool   // remove autostart and stop the process, but leave the installed files in place
}

// Uninstall reverses Install: removes the Startup shortcut, terminates any
// running copy, and (unless KeepFiles) deletes the installed files. The
// user's config.yaml lives in the same directory and goes with it.
func Uninstall(opts UninstallOptions) error {
	installDir, err := resolveInstallDir(opts.InstallDir)
	if err != nil {
		return err
	}

	fmt.Println("Removing it from the Startup folder...")
	if err := removeAutostart(); err != nil {
		return fmt.Errorf("removing autostart: %w", err)
	}

	fmt.Println("Stopping any running instance...")
	if err := terminateRunning(assetName); err != nil {
		return fmt.Errorf("stopping %s: %w", assetName, err)
	}

	if !opts.KeepFiles {
		fmt.Printf("Removing %s...\n", installDir)
		if err := os.RemoveAll(installDir); err != nil {
			return fmt.Errorf("removing %s: %w", installDir, err)
		}
	}

	fmt.Println("Done.")
	return nil
}

// latestRelease looks up the newest release through the GitHub API. The
// token, if any, is only ever sent here: it's what the API rate limit
// applies to, and the asset download itself needs no authentication.
func latestRelease(token string) (*ghRelease, error) {
	headers := []string{"Accept: application/vnd.github+json"}
	if token != "" {
		headers = append(headers, "Authorization: Bearer "+token)
	}
	var body bytes.Buffer
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	if err := httpGet(url, headers, &body); err != nil {
		return nil, fmt.Errorf("GitHub API: %w", err)
	}
	var rel ghRelease
	if err := json.Unmarshal(body.Bytes(), &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// downloadFile saves url's content to destPath, removing the file again
// if the download fails partway.
func downloadFile(url, destPath string) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if err := httpGet(url, nil, out); err != nil {
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

// startupDir is the current user's own Startup folder. Being per-user,
// writing there needs no administrator rights, and the user can see what's
// in it from Explorer.
func startupDir() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Startup, 0)
	if err != nil {
		return "", fmt.Errorf("locating the Startup folder: %w", err)
	}
	return dir, nil
}

func setAutostart(targetPath string) error {
	dir, err := startupDir()
	if err != nil {
		return err
	}
	if err := createShortcut(filepath.Join(dir, startupLinkName), targetPath, "Virtual Desktop Shortcuts"); err != nil {
		return err
	}
	return removeLegacyStartupExe(dir)
}

func removeAutostart() error {
	dir, err := startupDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, startupLinkName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return removeLegacyStartupExe(dir)
}

// removeLegacyStartupExe deletes the copy of the exe that versions up to
// v0.0.27 installed directly into the Startup folder, so the program isn't
// started twice after an update.
func removeLegacyStartupExe(dir string) error {
	path := filepath.Join(dir, legacyStartupExe)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// A leftover temp file from an interrupted update of that old layout.
	os.Remove(path + ".new")
	return nil
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
