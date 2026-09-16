# Details

Background on [Windows-Virtual-Desktop-Shortcuts](README.md): how it
works, how to configure and build it, and what it can't do.

## Configuration

`config.yaml` lives in `%LOCALAPPDATA%\VirtualDesktopShortcuts`, next to
the installed exe, and is created with defaults the first time the app
runs. The tray menu's **Configure** opens it in whatever application
Windows associates with `.yaml`. It currently has one setting:

| Setting   | Default | Meaning                                                |
| --------- | ------- | ------------------------------------------------------ |
| `enabled` | `true`  | Whether the shortcuts are active, same as the tray toggle |

Unknown keys are ignored, so a file written by another version still
loads, and an unreadable value falls back to that setting's default rather
than stopping the app. Edits are picked up within a couple of seconds.
Toggling from the tray writes the file back, preserving your comments.

### Command-line flags

Every setting also has a flag, which wins for that run without touching
the file:

| Flag                 | Meaning                                                  |
| -------------------- | -------------------------------------------------------- |
| `-enabled=false`     | Start with the shortcuts turned off                       |
| `-enable-logging`    | Append diagnostics to `VirtualDesktopShortcuts.log` next to the exe |

Logging is off by default and is only meant for troubleshooting.

## Tray icon

The icon is four tiles standing for virtual desktops, the first one
accented, drawn in code at runtime rather than loaded from an image file
(`internal/desktopicon` defines the shape, `internal/tray/icon.go` paints
it). While the shortcuts are off, the same glyph is drawn grey with a
diagonal red strike.

Explorer drops every tray icon when it restarts or crashes, so the tray
package listens for the `TaskbarCreated` broadcast and adds the icon back;
otherwise it would disappear for good until the app was restarted.

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
arrow for these shortcuts, so the focused app never sees them. Windows
ignores a hook that takes too long to answer, so the hook procedure only
classifies the keystroke and hands it to another goroutine over a
non-blocking channel; every COM call happens there.

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

## Repository layout

```
cmd/virtualdesktopshortcuts/  the tray app
cmd/vds-setup/                install/uninstall program
internal/hotkeys/             which keystroke means which desktop action
internal/hook/                the low-level keyboard hook
internal/vdesktop/            the undocumented virtual-desktop COM calls
internal/tray/                tray icon, menu, runtime-drawn glyph
internal/desktopicon/         the glyph's geometry, shared with tools/genicon
internal/config/              config.yaml loading, writing and watching
internal/win32/               Win32 declarations shared between packages
internal/setup/               install/uninstall, WinINet download, shortcut
internal/setupmenu/           setup's console menu (OS-independent, tested)
internal/singleinstance/      named-mutex guard
internal/applog/              opt-in log file next to the exe
tools/genicon/                renders the glyph to an .ico
```

The decision logic (`hotkeys`, `config`, `setupmenu`, `desktopicon`,
`genicon`) has no OS dependency and is covered by table tests that run
anywhere; the Windows-only packages are covered by vet and a
cross-compile.

## Building

Requires Go 1.23+. From the repo root:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o VirtualDesktopShortcuts.exe ./cmd/virtualdesktopshortcuts
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o Setup_VirtualDesktopShortcuts.exe ./cmd/vds-setup
```

(`-H=windowsgui` prevents a console window from flashing on startup; the
Setup program is a console program and deliberately omits it.) The build
has no cgo dependency, so it cross compiles cleanly from Linux/macOS as
well as natively on Windows.

To show a real version instead of `dev` in the tray tooltip, add
`-X main.version=v1.2.3` to `-ldflags` (the release workflow does this
automatically, using the pushed tag).

Checks, the same ones CI runs:

```sh
gofmt -l .
go test ./...
GOOS=windows GOARCH=amd64 go vet -unsafeptr=false ./...
```

`-unsafeptr=false` is deliberate: the `unsafe.Pointer` conversions of
LPARAM hook payloads, COM vtable slots and `CreateDIBSection`'s output
buffer are addresses supplied by Win32, outside anything Go's object graph
tracks. That's inherent to raw Win32 interop, and vet's static heuristic
can't tell it apart from a real misuse.

### Icon

Both exes carry the same file icon, generated from the same glyph the tray
uses and committed as a `rsrc_windows_amd64.syso` per `cmd` directory,
which `go build` links in automatically. To regenerate after changing
`internal/desktopicon`:

```sh
go run ./tools/genicon desktops.ico
go run github.com/akavel/rsrc@latest -ico desktops.ico -arch amd64 -o cmd/virtualdesktopshortcuts/rsrc_windows_amd64.syso
go run github.com/akavel/rsrc@latest -ico desktops.ico -arch amd64 -o cmd/vds-setup/rsrc_windows_amd64.syso
```

## Running

You can run `VirtualDesktopShortcuts.exe` directly from anywhere, or use
the Setup program below to install it and start it at sign-in.

Only one instance runs at a time: a named mutex makes a second copy show a
message and exit.

## Setup tool (`cmd/vds-setup`)

`Setup_VirtualDesktopShortcuts.exe` is a small console program that, when
run, asks what to do:

```
Virtual Desktop Shortcuts - Setup

  1) Install / update
  2) Uninstall

Choose an option [1-2] (installing/updating automatically in 5 seconds if nothing is chosen):
```

- **1) Install / update** downloads the newest GitHub release into
  `%LOCALAPPDATA%\VirtualDesktopShortcuts`, adds a shortcut to your
  Startup folder (`FOLDERID_Startup`, written through the shell's
  `IShellLink`, exactly like dragging a program in there yourself) and
  starts it. This is also the default: if nothing is chosen within 5
  seconds, it runs on its own, which suits unattended or scripted setup.
- **2) Uninstall** removes the Startup shortcut, stops the running app and
  deletes the installed directory, `config.yaml` included.

When the action was auto-chosen and succeeded, the window closes itself
after 3 seconds; otherwise it waits for Enter, so an error stays readable.

Downloads go through **WinINet**, Windows' own HTTP stack, rather than
Go's `net/http`: that uses the system proxy settings and certificate
store, and keeps several MB of TLS code out of the exe.

It's safe to run any time, including repeatedly: install/update always
writes the same fixed path and shortcut name, so it never creates
duplicates, and it stops any running copy before replacing the file.
Installs from v0.0.27 and earlier put the exe straight into the Startup
folder; that copy is removed on both install and uninstall, so the app
can't end up starting twice.

Flags for scripted use:

| Flag                 | Meaning                                                       |
| -------------------- | ------------------------------------------------------------- |
| `-mode install`      | Install or update without showing the menu                     |
| `-mode uninstall`    | Uninstall without showing the menu                             |
| `-install-dir DIR`   | Use DIR instead of `%LOCALAPPDATA%\VirtualDesktopShortcuts`    |
| `-github-token TOK`  | Avoid the unauthenticated GitHub API rate limit (install only) |
| `-no-launch`         | Install, but don't start it now (install only)                 |
| `-no-autostart`      | Install, but don't add the Startup shortcut (install only)     |
| `-keep-files`        | Uninstall, but leave the installed files in place              |

## Releases

Two workflows:

- [`ci.yml`](.github/workflows/ci.yml) runs on every push and pull request
  to `main`: tests, vet and a cross-compile, publishing nothing.
- [`build.yml`](.github/workflows/build.yml) runs on `v*` tags: the same
  checks, then builds both exes with the tag stamped in as the version and
  attaches them to a GitHub Release.

```sh
git tag v1.0.0
git push origin v1.0.0
```

## Limitations

- Windows only (10 1507+ / 11), amd64.
- Relies on undocumented Windows internals (see above) — if a Windows
  update breaks it, the fix is updating the interface IDs in
  `internal/vdesktop`.
- Does not run elevated, and does not need to — but if the currently
  focused window is running as Administrator, the low-level hook may not
  reliably intercept keystrokes while that window has focus, due to
  Windows' User Interface Privilege Isolation (UIPI). Run this app elevated
  too if you need it to work while elevated windows are focused.
