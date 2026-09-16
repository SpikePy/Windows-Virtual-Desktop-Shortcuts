//go:build windows

// Package autostart keeps the shortcut in the user's own Startup folder in
// line with the autostart setting: exactly one VirtualDesktopShortcuts.lnk
// pointing at the installed exe while it's on, none while it's off. Both
// the app and the Setup program use it.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// linkName is the shortcut in the Startup folder. Versions up to
	// v0.0.27 put the exe itself there instead; legacyExe exists only to
	// clean that up.
	linkName  = "VirtualDesktopShortcuts.lnk"
	legacyExe = "VirtualDesktopShortcuts.exe"
)

// Apply makes the Startup folder match enabled: a shortcut to target when
// it's on, no shortcut when it's off. A shortcut that already points at
// target is left alone, so calling this on every config change is cheap.
// If target doesn't exist (e.g. a build run from somewhere else) no
// shortcut is created, since it would only point at nothing.
func Apply(enabled bool, target string) error {
	// COM is initialised per OS thread, so this goroutine must not move
	// between CoInitializeEx and CoUninitialize.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir, err := windows.KnownFolderPath(windows.FOLDERID_Startup, 0)
	if err != nil {
		return fmt.Errorf("locating the Startup folder: %w", err)
	}
	link := filepath.Join(dir, linkName)
	removeLegacyExe(dir)

	if !enabled {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("not adding autostart for %s: %w", target, err)
	}
	if current, err := shortcutTarget(link); err == nil && strings.EqualFold(current, target) {
		return nil
	}
	return createShortcut(link, target, "Virtual Desktop Shortcuts")
}

// removeLegacyExe deletes the copy of the exe that versions up to v0.0.27
// installed directly into the Startup folder, so the program isn't
// started twice.
func removeLegacyExe(dir string) {
	path := filepath.Join(dir, legacyExe)
	os.Remove(path)
	// A leftover temp file from an interrupted update of that old layout.
	os.Remove(path + ".new")
}

// The shortcut is written through the shell's IShellLink COM object - the
// same file Explorer writes when you drag a program in there.
var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
)

// The shell's class and interface IDs, all of the form
// {xxxxxxxx-0000-0000-C000-000000000046}.
var (
	clsidShellLink = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

const (
	clsctxInprocServer = 0x1
	stgmRead           = 0x0
)

// comObject is any COM interface pointer: its first field is the vtable.
type comObject struct{ vtbl uintptr }

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// iShellLinkWVtbl and iPersistFileVtbl mirror those interfaces' methods.
// Only the ones called below matter by name, but the order must match the
// interface exactly - that order is what picks the function to call.
type iShellLinkWVtbl struct {
	iUnknownVtbl
	GetPath             uintptr
	GetIDList           uintptr
	SetIDList           uintptr
	GetDescription      uintptr
	SetDescription      uintptr
	GetWorkingDirectory uintptr
	SetWorkingDirectory uintptr
	GetArguments        uintptr
	SetArguments        uintptr
	GetHotkey           uintptr
	SetHotkey           uintptr
	GetShowCmd          uintptr
	SetShowCmd          uintptr
	GetIconLocation     uintptr
	SetIconLocation     uintptr
	SetRelativePath     uintptr
	Resolve             uintptr
	SetPath             uintptr
}

type iPersistFileVtbl struct {
	iUnknownVtbl
	GetClassID    uintptr
	IsDirty       uintptr
	Load          uintptr
	Save          uintptr
	SaveCompleted uintptr
	GetCurFile    uintptr
}

func (o *comObject) release() {
	syscall.SyscallN((*iUnknownVtbl)(unsafe.Pointer(o.vtbl)).Release, uintptr(unsafe.Pointer(o)))
}

// comSetUp initialises COM for this thread and returns the matching
// tear-down. A thread already initialised in another mode is fine - it
// just isn't ours to tear down.
func comSetUp() (func(), error) {
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if err == nil {
		return windows.CoUninitialize, nil
	}
	if errors.Is(err, syscall.Errno(windows.RPC_E_CHANGED_MODE)) {
		return func() {}, nil
	}
	return func() {}, fmt.Errorf("CoInitializeEx: %w", err)
}

// newShellLink creates an IShellLinkW instance and returns it with its
// vtable and a release function.
func newShellLink() (*comObject, *iShellLinkWVtbl, func(), error) {
	var link *comObject
	if hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&link))); hr != 0 {
		return nil, nil, nil, fmt.Errorf("CoCreateInstance(ShellLink): 0x%08X", hr)
	}
	return link, (*iShellLinkWVtbl)(unsafe.Pointer(link.vtbl)), link.release, nil
}

// createShortcut writes a .lnk at lnkPath pointing at target.
func createShortcut(lnkPath, target, description string) error {
	comDone, err := comSetUp()
	if err != nil {
		return err
	}
	defer comDone()

	link, vtbl, releaseLink, err := newShellLink()
	if err != nil {
		return err
	}
	defer releaseLink()

	set := func(method uintptr, name, value string) error {
		p, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		if hr, _, _ := syscall.SyscallN(method, uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(p))); hr != 0 {
			return fmt.Errorf("IShellLink::%s(%s): 0x%08X", name, value, hr)
		}
		return nil
	}
	if err := set(vtbl.SetPath, "SetPath", target); err != nil {
		return err
	}
	if err := set(vtbl.SetWorkingDirectory, "SetWorkingDirectory", filepath.Dir(target)); err != nil {
		return err
	}
	if err := set(vtbl.SetDescription, "SetDescription", description); err != nil {
		return err
	}

	var persist *comObject
	if hr, _, _ := syscall.SyscallN(vtbl.QueryInterface,
		uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&persist))); hr != 0 {
		return fmt.Errorf("IShellLink::QueryInterface(IPersistFile): 0x%08X", hr)
	}
	defer persist.release()

	lnkPtr, err := windows.UTF16PtrFromString(lnkPath)
	if err != nil {
		return err
	}
	persistVtbl := (*iPersistFileVtbl)(unsafe.Pointer(persist.vtbl))
	if hr, _, _ := syscall.SyscallN(persistVtbl.Save,
		uintptr(unsafe.Pointer(persist)), uintptr(unsafe.Pointer(lnkPtr)), 1); hr != 0 {
		return fmt.Errorf("IPersistFile::Save(%s): 0x%08X", lnkPath, hr)
	}
	return nil
}

// shortcutTarget reads back the program a .lnk points at.
func shortcutTarget(lnkPath string) (string, error) {
	comDone, err := comSetUp()
	if err != nil {
		return "", err
	}
	defer comDone()

	link, vtbl, releaseLink, err := newShellLink()
	if err != nil {
		return "", err
	}
	defer releaseLink()

	var persist *comObject
	if hr, _, _ := syscall.SyscallN(vtbl.QueryInterface,
		uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&persist))); hr != 0 {
		return "", fmt.Errorf("IShellLink::QueryInterface(IPersistFile): 0x%08X", hr)
	}
	defer persist.release()

	lnkPtr, err := windows.UTF16PtrFromString(lnkPath)
	if err != nil {
		return "", err
	}
	persistVtbl := (*iPersistFileVtbl)(unsafe.Pointer(persist.vtbl))
	if hr, _, _ := syscall.SyscallN(persistVtbl.Load,
		uintptr(unsafe.Pointer(persist)), uintptr(unsafe.Pointer(lnkPtr)), stgmRead); hr != 0 {
		return "", fmt.Errorf("IPersistFile::Load(%s): 0x%08X", lnkPath, hr)
	}

	buf := make([]uint16, windows.MAX_PATH)
	if hr, _, _ := syscall.SyscallN(vtbl.GetPath,
		uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); hr != 0 {
		return "", fmt.Errorf("IShellLink::GetPath: 0x%08X", hr)
	}
	return windows.UTF16ToString(buf), nil
}
