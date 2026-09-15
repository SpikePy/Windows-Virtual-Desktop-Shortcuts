# Windows-Virtual-Desktop-Shortcuts

A tiny Windows-only background utility that lets you jump straight to the
Nth virtual desktop with **Win+1** through **Win+9** (the same way
`Win+1..9` already jumps to the Nth pinned taskbar app) or to the
previous/next one with **Win+Left**/**Win+Right**, and move the focused
window there by adding **Shift**.

- `Win+1` … `Win+9` → switch to virtual desktop 1 … 9
- `Win+Shift+1` … `Win+Shift+9` → move the focused window to virtual
  desktop 1 … 9 (without switching to it)
- `Win+Left` / `Win+Right` → switch to the previous / next virtual desktop
- `Win+Shift+Left` / `Win+Shift+Right` → move the focused window to the
  previous / next virtual desktop and switch along with it, so pressing it
  again keeps moving the same window

If the desktop doesn't exist (e.g. you press `Win+5` but only have 3
desktops, or `Win+Right` on the last one), the shortcut is a no-op — it
does not create a new desktop or wrap around, and moving with no window
focused is also a no-op. These replace Windows' own `Win+Left`/`Win+Right`
(snap the window to one side) and `Win+Shift+Left`/`Win+Shift+Right` (move
the window to another monitor); `Win+Ctrl+Left`/`Win+Ctrl+Right` are left
to Windows.

**Explorer's own shortcuts on these keys:** Explorer uses Win+1..9 to open
pinned taskbar apps and Win+Left/Right to snap windows, and this app can't
fully override that (see [below](#how-it-works-and-why-its-fragile)):
without turning it off, Win+N minimizes the focused app instead of
switching when that app is pinned at taskbar position N.
`Setup_VirtualDesktopShortcuts.exe` turns these shortcuts off when
installing, by adding `123456789%'` to Explorer's `DisabledHotkeys`
registry value (each character is a key's virtual-key code; `%` is Left
and `'` is Right), and back on when uninstalling, restarting Explorer
whenever the value changes. While they're off, Windows doesn't act on
those keys itself, even if this app is disabled or not running.

If you don't use the Setup tool, set it by hand, then restart Explorer or
sign out and back in:

```
reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\Advanced" /v DisabledHotkeys /t REG_SZ /d "123456789%'" /f
```

and undo it with:

```
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\Advanced" /v DisabledHotkeys /f
```

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

Similarly, `Win+<digit>` is a shortcut reserved by Explorer for launching,
switching to, or (if it's already the active window) minimizing the Nth
pinned taskbar app, so the OS refuses to let a normal app register it with
`RegisterHotKey` (the same goes for Win+Left/Right, used for snapping).
Instead this app installs a low-level keyboard hook (`WH_KEYBOARD_LL`) that
swallows the key-down and key-up of the digit or arrow for these shortcuts,
with or without Shift; Ctrl or Alt held down with the key is left alone,
so combinations like Ctrl+Win+3 and Windows' own Ctrl+Win+Left/Right keep
working normally. While
Win is still held it also taps an unassigned virtual key (0xE8), the same
"menu mask key" trick AutoHotkey uses, so releasing Win doesn't open the
Start menu.

The hook alone isn't enough, though. Explorer still reacts to Win+digit
even with the digit swallowed: when the app pinned at that taskbar
position is already focused, it minimizes the app instead of letting the
desktop switch. Swallowing the Win key's own key-up and trying to
un-minimize the app afterwards were both tried and neither helped. The
fix is the `DisabledHotkeys` setting described above, which
stops Explorer from handling these keys at all.

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
