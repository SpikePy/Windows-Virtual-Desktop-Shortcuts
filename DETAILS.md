# Details

Background on [Windows-Virtual-Desktop-Shortcuts](README.md): how it
works, how to configure and build it, and what it can't do.

## Tray icon and configuration

The app runs quietly in the system tray, showing its name and version on
hover. **Left-click** the tray icon to toggle Enable/Disable directly.
When disabled, the icon turns grey with a red diagonal strike-through so
you can tell at a glance.

**Right-click** for the full menu:

- **Enable** / **Disable** — same toggle as left-click, spelled out
  (whichever state is already active is greyed out).
- **Configure** — opens `config.yaml` (creating it with defaults on first
  use) in your default YAML editor. It lives at
  `%APPDATA%\VirtualDesktopShortcuts\config.yaml` and currently has one
  setting, `enabled`, which mirrors the tray's Enable/Disable — edit and
  save it and the change takes effect within a couple of seconds, no
  restart needed (including updating the tray icon).
- **Exit** — quits the app.

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

The shortcuts are handled by a low-level keyboard hook (`WH_KEYBOARD_LL`)
rather than `RegisterHotKey`, because a registered Ctrl+Alt hotkey would
also fire for AltGr and take away the characters it types. The hook checks
which Alt key is held and swallows the key-down and key-up of the digit or
arrow for these shortcuts, so the focused app never sees them.

Moving a window to a desktop *could* use the one piece of this that's a
documented, public API — `IVirtualDesktopManager::MoveWindowToDesktop` —
but in practice that reliably fails with `E_ACCESSDENIED` for windows
outside the calling process (a widely-reported limitation, not something
specific to this app). Instead this app uses the same undocumented
mechanism real tools like VirtualDesktopAccessor use:
`IVirtualDesktopManagerInternal::MoveViewToDesktop`, which operates on an
`IApplicationView` (resolved from the target HWND via
`IApplicationViewCollection::GetViewForHwnd`) rather than a raw HWND, and
actually works.

## Building

Requires Go 1.23+. From the repo root:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H=windowsgui" -o VirtualDesktopShortcuts.exe .
```

(`-H=windowsgui` prevents a console window from flashing on startup; it's
optional during development.) The build has no cgo dependency, so it cross
compiles cleanly from Linux/macOS as well as natively on Windows.

To show a real version instead of `dev` in the tray tooltip, add
`-X main.version=v1.2.3` to `-ldflags` (the release workflow does this
automatically, using the pushed tag).

### Icon

Both `VirtualDesktopShortcuts.exe` and `Setup_VirtualDesktopShortcuts.exe`
share the same icon, embedded as a Windows resource via
[go-winres](https://github.com/tc-hib/go-winres): each has its own
`winres/` directory and checked-in `rsrc_windows_amd64.syso` that
`go build` links in automatically — no extra build step needed. The root
`winres/winres.json` embeds two named icon groups, `APP` (the normal
blue-tile icon) and `APPDISABLED` (all tiles grey, with a red diagonal
strike-through); `setup/winres/` only needs `APP`. The main app loads both
by name at runtime (`loadNamedIcon` in `tray_windows.go`) and swaps
between them for the tray icon based on Enable/Disable state.

To change the icon, replace the PNGs under `winres/` **and**
`setup/winres/` and regenerate both:

```sh
go install github.com/tc-hib/go-winres@latest
go-winres make --arch amd64 --out rsrc
(cd setup && go-winres make --arch amd64 --out rsrc)
```

## Running

You can just run `VirtualDesktopShortcuts.exe` directly — copy it wherever you
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
Enter choice [1-2] (defaults to Install/update in 5s):
```

- **1) Install / update** downloads the newest GitHub release of this tool
  and installs it into your Startup folder
  (`%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup`), so it's
  always running and always launches automatically at sign-in — no manual
  copying required. Running this again later re-checks and updates it.
  This is also the default: if nothing is chosen within 5 seconds of the
  prompt appearing, it runs automatically (handy for unattended/scripted
  first-time setup).
- **2) Uninstall** stops the running app and removes it from the Startup
  folder, leaving nothing installed and nothing running.

Once the chosen action finishes, the window closes itself automatically
after 3 seconds (press Enter to close it immediately instead).

It's safe to run any time, including repeatedly (e.g. from a scheduled
task, to keep the tool updated): neither action ever creates duplicates.

- **No duplicate installs**: install/update always writes to the same
  fixed path, and first compares the SHA-256 of the downloaded build
  against the installed one — if they match, it skips reinstalling
  entirely.
- **No duplicate processes**: before writing a new build over the old one,
  install/update stops every running `VirtualDesktopShortcuts.exe` process
  by name; afterwards it makes sure exactly one instance is running,
  starting it if it wasn't. Uninstall stops every running instance too.

For scripting/automation, `--install` and `--uninstall` flags skip the
prompt and run that action directly (e.g. `Setup_VirtualDesktopShortcuts.exe --install`).

Build it yourself with:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o Setup_VirtualDesktopShortcuts.exe ./setup
```

## Releases

Pushing a tag matching `v*` (e.g. `v1.0.0`) triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which
cross-compiles both `VirtualDesktopShortcuts.exe` and
`Setup_VirtualDesktopShortcuts.exe` and attaches both plain `.exe` files
(no zip) as workflow artifacts and to a GitHub Release for that tag.

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
