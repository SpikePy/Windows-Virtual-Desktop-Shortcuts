# Windows-Virtual-Desktop-Shortcuts

A tiny Windows-only background utility that lets you jump straight to the
Nth virtual desktop with **Win+1** through **Win+9**, the same way
`Win+1..9` already jumps to the Nth pinned taskbar app.

- `Win+1` → switch to virtual desktop 1
- `Win+2` → switch to virtual desktop 2
- ...
- `Win+9` → switch to virtual desktop 9

If the desktop doesn't exist (e.g. you press `Win+5` but only have 3
desktops), the shortcut is a no-op — it does not create a new desktop.

The app runs quietly in the system tray. Right-click the tray icon and
choose **Exit** to quit.

## How it works, and why it's fragile

Windows does not have a public, documented API for switching directly to
the Nth virtual desktop, or for listing/enumerating desktops. The only way
to do this (the same technique every other "switch desktop by number" tool
uses, e.g. [VirtualDesktopAccessor](https://github.com/Ciantic/VirtualDesktopAccessor))
is via **undocumented COM interfaces** that `explorer.exe` happens to
expose. Microsoft is free to change these at any time, and has changed them
before — this app detects whether it's running on Windows 10 (builds
10240–21999) or Windows 11 (builds 22000+) and uses the matching interface
IDs for each, but a future Windows feature update could still break it.

Similarly, `Win+<digit>` is a shortcut reserved by Explorer for launching
pinned taskbar apps, so the OS refuses to let a normal app register it with
`RegisterHotKey`. Instead this app installs a low-level keyboard hook
(`WH_KEYBOARD_LL`) that intercepts the keystroke before Explorer sees it,
and swallows it — but only when Win+digit is pressed with no other
modifiers held, so combinations like Ctrl+Win+3 are left alone.

## Building

Requires Go 1.23+. From the repo root:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H=windowsgui" -o vdesktop-switcher.exe .
```

(`-H=windowsgui` prevents a console window from flashing on startup; it's
optional during development.) The build has no cgo dependency, so it cross
compiles cleanly from Linux/macOS as well as natively on Windows.

## Running

You can just run `vdesktop-switcher.exe` directly — copy it wherever you
like and optionally add a shortcut to your Startup folder (`shell:startup`)
if you want it to launch automatically when you sign in. Or use
`Setup_VirtualDesktopShortcuts.exe` (below) to do that for you.

Only one instance runs at a time; launching a second copy shows a message
box and exits.

## Setup tool (`setup/`)

`Setup_VirtualDesktopShortcuts.exe` ([`setup/main.go`](setup/main.go)) is a
small console tool that, when run, asks what to do:

```
Virtual Desktop Shortcuts - Setup
==================================

What would you like to do?
  1) Install / update
  2) Uninstall
  3) Exit
Enter choice [1-3]:
```

- **1) Install / update** downloads the newest GitHub release of this tool
  and installs it into your Startup folder
  (`%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup`), so it's
  always running and always launches automatically at sign-in — no manual
  copying required. Running this again later re-checks and updates it.
- **2) Uninstall** stops the running app and removes it from the Startup
  folder, leaving nothing installed and nothing running.

It's safe to run any time, including repeatedly (e.g. from a scheduled
task, to keep the tool updated): neither action ever creates duplicates.

- **No duplicate installs**: install/update always writes to the same
  fixed path, and first compares the SHA-256 of the downloaded build
  against the installed one — if they match, it skips reinstalling
  entirely.
- **No duplicate processes**: before writing a new build over the old one,
  install/update stops every running `vdesktop-switcher.exe` process by
  name; afterwards it makes sure exactly one instance is running, starting
  it if it wasn't. Uninstall stops every running instance too.

For scripting/automation, `--install` and `--uninstall` flags skip the
prompt and run that action directly (e.g. `Setup_VirtualDesktopShortcuts.exe --install`).

Build it yourself with:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o Setup_VirtualDesktopShortcuts.exe ./setup
```

## Releases

Pushing a tag matching `v*` (e.g. `v1.0.0`) triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which
cross-compiles both `vdesktop-switcher.exe` and
`Setup_VirtualDesktopShortcuts.exe`, uploads them as workflow artifacts,
and attaches them (the app zipped, the setup tool as a plain `.exe`) to a
GitHub Release for that tag.

```sh
git tag v1.0.0
git push origin v1.0.0
```

## Limitations

- Windows only (10 1507+ / 11), amd64.
- Relies on undocumented Windows internals (see above) — if a Windows
  update breaks it, the fix is updating the interface IDs in
  `vdesktop_windows.go`.
- Does not run elevated, and does not need to — but if the currently
  focused window is running as Administrator, the low-level hook may not
  reliably intercept keystrokes while that window has focus, due to
  Windows' User Interface Privilege Isolation (UIPI). Run this app elevated
  too if you need it to work while elevated windows are focused.
