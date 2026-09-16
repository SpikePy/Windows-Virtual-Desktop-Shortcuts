# Windows-Virtual-Desktop-Shortcuts

A tiny Windows-only background utility that switches virtual desktops by
number and moves windows between them, using Ctrl+Alt shortcuts.

- `Ctrl+Alt+1` … `Ctrl+Alt+9` → switch to virtual desktop 1 … 9
- `Ctrl+Alt+Shift+1` … `Ctrl+Alt+Shift+9` → move the focused window to
  virtual desktop 1 … 9 (without switching to it)
- `Ctrl+Alt+Left` / `Ctrl+Alt+Right` → switch to the previous / next
  virtual desktop
- `Ctrl+Alt+Shift+Left` / `Ctrl+Alt+Shift+Right` → move the focused window
  to the previous / next virtual desktop and switch along with it, so
  pressing it again keeps moving the same window
- `Ctrl+Alt+scroll up` / `Ctrl+Alt+scroll down` → switch to the previous /
  next virtual desktop, one per wheel notch (add `Shift` to take the
  focused window along)

Use the **left** Alt key. On layouts where the right Alt key is AltGr
(German, for example), AltGr counts as Ctrl+Alt and types characters like
`{` and `[` with the digit keys, so it's ignored and keeps typing as usual.

If the desktop doesn't exist (e.g. you press `Ctrl+Alt+5` but only have 3
desktops, or `Ctrl+Alt+Right` on the last one), nothing happens — it does
not create a new desktop or wrap around, and moving with no window focused
does nothing either.

## Install

Download `Setup_VirtualDesktopShortcuts.exe` from the
[latest release](https://github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/releases/latest)
and run it. It installs the app into `%LOCALAPPDATA%\VirtualDesktopShortcuts`
and starts it. While the `autostart` setting is on (the default), the app
keeps a shortcut to itself in your Startup folder, so it starts
automatically when you sign in; turn the setting off and the shortcut is
removed. Run it again any time to update to the
newest release, or choose *Uninstall* to remove it.

Nothing here needs administrator rights, and nothing is installed for
other users. You can also just run `VirtualDesktopShortcuts.exe` yourself,
from anywhere you like.

## Tray icon

The app runs in the system tray, showing its name, version and whether it's
enabled or disabled on hover. **Left-click** the icon to turn the
shortcuts on or off; while they're off the icon is grey with a red
strike-through. **Right-click** for a menu with
Enable/Disable, Configure and Exit.

Configure opens `config.yaml` in
`%LOCALAPPDATA%\VirtualDesktopShortcuts`, which has two settings:
`enabled`, mirroring the tray toggle, and `autostart`, whether it starts
when you sign in. Edits are picked up within a couple of seconds,
no restart needed.

## More

[DETAILS.md](DETAILS.md) covers how it works, the command-line flags,
building from source, the Setup tool, releases and known limitations.
