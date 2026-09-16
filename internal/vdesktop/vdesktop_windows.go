//go:build windows

// Package vdesktop switches virtual desktops and moves windows between
// them.
//
// None of this is a public, documented Windows API. It relies on
// undocumented COM interfaces exposed by explorer.exe that Microsoft can
// (and periodically does) change between releases. The interface IDs below
// differ between the "Windows 10 era" (builds 10240-21999) and the
// "Windows 11 era" (builds 22000+), but the vtable method order has stayed
// stable across both eras, which is what this code relies on.
//
// Sources cross-checked against the (differently-licensed, not vendored
// here) Ciantic/VirtualDesktopAccessor project, which maintains the most
// complete public record of these interfaces:
//
//	https://github.com/Ciantic/VirtualDesktopAccessor
package vdesktop

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/hotkeys"
)

var (
	modOle32  = windows.NewLazySystemDLL("ole32.dll")
	modUser32 = windows.NewLazySystemDLL("user32.dll")

	procCoCreateInstance    = modOle32.NewProc("CoCreateInstance")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
)

var (
	clsidImmersiveShell                = guid("{C2F03A33-21F5-47FA-B4BB-156362A2F239}")
	clsidVirtualDesktopManagerInternal = guid("{C5E0CDCA-7B6E-41B2-9FC4-D93975CC467B}")
	iidIServiceProvider                = guid("{6D5140C1-7436-11CE-8034-00AA006009FA}")

	// Windows 10 (builds 10240-21999).
	iidVirtualDesktopManagerInternalWin10 = guid("{0F3A72B0-4566-487E-9A33-4ED302F6D6CE}")
	iidVirtualDesktopWin10                = guid("{FF72FFDD-BE7E-43FC-9C03-AD81681E88E4}")

	// Windows 11 (builds 22000+).
	iidVirtualDesktopManagerInternalWin11 = guid("{53F5CA0B-158F-4124-900C-057158060B27}")
	iidVirtualDesktopWin11                = guid("{3F07F4BE-B107-441A-AF0F-39D82529072C}")

	// IApplicationViewCollection is what turns a plain HWND into the
	// IApplicationView that move_view_to_desktop needs. Same IID on every
	// Windows version, and - unusually for this file - it's queried from
	// IServiceProvider using its own IID as both the "service" and the
	// requested interface.
	iidIApplicationViewCollection = guid("{1841C6D7-4F9D-42C0-AF41-8747538F10E5}")
)

// vtable slot indices (0-based, counting QueryInterface=0, AddRef=1,
// Release=2 as the first three slots shared by every COM interface).
const (
	slotServiceProviderQueryService = 3

	slotManagerInternalMoveViewToDesktop = 4
	slotManagerInternalGetCurrentDesktop = 6
	slotManagerInternalGetDesktops       = 7
	slotManagerInternalSwitchDesktop     = 9

	slotVirtualDesktopGetID = 4

	slotObjectArrayGetCount = 3
	slotObjectArrayGetAt    = 4

	slotAppViewCollectionGetViewForHwnd = 6

	clsctxLocalServer = 0x4
)

func guid(s string) *windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(fmt.Sprintf("invalid GUID %q: %v", s, err))
	}
	return &g
}

// comCall invokes the vtable method at the given zero-based slot on a raw
// COM interface pointer, using the standard COM object layout where the
// first machine word at the object address points to its vtable.
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(obj)
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	full := make([]uintptr, 0, len(args)+1)
	full = append(full, uintptr(obj))
	full = append(full, args...)
	r0, _, _ := syscall.SyscallN(fn, full...)
	return r0
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

func hrFailed(hr uintptr) bool { return int32(hr) < 0 }

func coCreateInstance(clsid *windows.GUID, clsCtx uint32, iid *windows.GUID) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	r0, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)),
		0,
		uintptr(clsCtx),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if hrFailed(r0) || obj == nil {
		return nil, fmt.Errorf("CoCreateInstance: hr=0x%08X", uint32(r0))
	}
	return obj, nil
}

func foregroundWindow() uintptr {
	r0, _, _ := procGetForegroundWindow.Call()
	return r0
}

// Switcher owns the COM apartment used to talk to the virtual desktop
// manager. It must live on a single, dedicated OS thread for the lifetime
// of the apartment (COINIT_APARTMENTTHREADED objects are not thread-safe
// and must be used from the thread that created them).
type Switcher struct {
	iidManagerInternal *windows.GUID
	iidVirtualDesktop  *windows.GUID
}

// Run processes requests until the channel is closed, reporting failures
// through logf. It must be started via `go vdesktop.Run(...)` from a
// goroutine that is allowed to own an OS thread indefinitely.
func Run(requests <-chan hotkeys.Request, logf func(format string, args ...any)) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		logf("EXCEPTION initializing COM: %v", err)
		return
	}
	defer windows.CoUninitialize()

	sw := &Switcher{
		iidManagerInternal: iidVirtualDesktopManagerInternalWin10,
		iidVirtualDesktop:  iidVirtualDesktopWin10,
	}
	if windows.RtlGetVersion().BuildNumber >= 22000 {
		sw.iidManagerInternal = iidVirtualDesktopManagerInternalWin11
		sw.iidVirtualDesktop = iidVirtualDesktopWin11
	}

	for req := range requests {
		if err := sw.Handle(req); err != nil {
			logf("request %+v failed: %v", req, err)
		}
	}
}

// Handle resolves a request's target desktop and carries it out. Relative
// requests past the first or last desktop do nothing.
func (sw *Switcher) Handle(req hotkeys.Request) error {
	index := req.Index
	if req.Relative {
		current, err := sw.currentDesktopIndex()
		if err != nil {
			return err
		}
		index += current
		if index < 0 {
			return nil
		}
	}

	switch {
	case req.Action == hotkeys.ActionSwitch:
		return sw.SwitchTo(index)
	case !req.Relative:
		_, err := sw.MoveWindowTo(foregroundWindow(), index)
		return err
	default:
		// Ctrl+Alt+Shift+Left/Right takes the window along and keeps it
		// focused, so pressing it again keeps moving the same window.
		hwnd := foregroundWindow()
		moved, err := sw.MoveWindowTo(hwnd, index)
		if err != nil || !moved {
			return err
		}
		if err := sw.SwitchTo(index); err != nil {
			return err
		}
		procSetForegroundWindow.Call(hwnd)
		return nil
	}
}

// queryManagerInternal fetches the undocumented
// VirtualDesktopManagerInternal service through the ImmersiveShell's
// IServiceProvider. Every action in this file starts here.
func (sw *Switcher) queryManagerInternal(provider unsafe.Pointer) (unsafe.Pointer, error) {
	var managerInternal unsafe.Pointer
	hr := comCall(provider, slotServiceProviderQueryService,
		uintptr(unsafe.Pointer(clsidVirtualDesktopManagerInternal)),
		uintptr(unsafe.Pointer(sw.iidManagerInternal)),
		uintptr(unsafe.Pointer(&managerInternal)),
	)
	if hrFailed(hr) || managerInternal == nil {
		return nil, fmt.Errorf("query VirtualDesktopManagerInternal service: hr=0x%08X", uint32(hr))
	}
	return managerInternal, nil
}

// desktopAt returns the IVirtualDesktop COM pointer at the given
// zero-based index (the caller must Release it), or nil if no such desktop
// exists.
func (sw *Switcher) desktopAt(managerInternal unsafe.Pointer, zeroBasedIndex int) (unsafe.Pointer, error) {
	objArray, count, err := sw.desktops(managerInternal)
	if err != nil {
		return nil, err
	}
	defer comRelease(objArray)

	if zeroBasedIndex < 0 || uint32(zeroBasedIndex) >= count {
		// Desktop N doesn't exist.
		return nil, nil
	}

	var desktop unsafe.Pointer
	hr := comCall(objArray, slotObjectArrayGetAt,
		uintptr(uint32(zeroBasedIndex)),
		uintptr(unsafe.Pointer(sw.iidVirtualDesktop)),
		uintptr(unsafe.Pointer(&desktop)),
	)
	if hrFailed(hr) || desktop == nil {
		return nil, fmt.Errorf("IObjectArray::GetAt(%d): hr=0x%08X", zeroBasedIndex, uint32(hr))
	}
	return desktop, nil
}

// desktops returns the IObjectArray of every desktop (the caller must
// Release it) and how many there are.
func (sw *Switcher) desktops(managerInternal unsafe.Pointer) (unsafe.Pointer, uint32, error) {
	var objArray unsafe.Pointer
	hr := comCall(managerInternal, slotManagerInternalGetDesktops, uintptr(unsafe.Pointer(&objArray)))
	if hrFailed(hr) || objArray == nil {
		return nil, 0, fmt.Errorf("get_desktops: hr=0x%08X", uint32(hr))
	}

	var count uint32
	if hr := comCall(objArray, slotObjectArrayGetCount, uintptr(unsafe.Pointer(&count))); hrFailed(hr) {
		comRelease(objArray)
		return nil, 0, fmt.Errorf("IObjectArray::GetCount: hr=0x%08X", uint32(hr))
	}
	return objArray, count, nil
}

// desktopID returns the GUID identifying an IVirtualDesktop.
func desktopID(desktop unsafe.Pointer) (windows.GUID, error) {
	var id windows.GUID
	if hr := comCall(desktop, slotVirtualDesktopGetID, uintptr(unsafe.Pointer(&id))); hrFailed(hr) {
		return windows.GUID{}, fmt.Errorf("IVirtualDesktop::GetID: hr=0x%08X", uint32(hr))
	}
	return id, nil
}

// currentDesktopIndex returns the zero-based index of the desktop
// currently shown.
func (sw *Switcher) currentDesktopIndex() (int, error) {
	provider, err := coCreateInstance(clsidImmersiveShell, clsctxLocalServer, iidIServiceProvider)
	if err != nil {
		return 0, fmt.Errorf("create ImmersiveShell instance: %w", err)
	}
	defer comRelease(provider)

	managerInternal, err := sw.queryManagerInternal(provider)
	if err != nil {
		return 0, err
	}
	defer comRelease(managerInternal)

	var current unsafe.Pointer
	hr := comCall(managerInternal, slotManagerInternalGetCurrentDesktop, uintptr(unsafe.Pointer(&current)))
	if hrFailed(hr) || current == nil {
		return 0, fmt.Errorf("get_current_desktop: hr=0x%08X", uint32(hr))
	}
	currentID, err := desktopID(current)
	comRelease(current)
	if err != nil {
		return 0, err
	}

	objArray, count, err := sw.desktops(managerInternal)
	if err != nil {
		return 0, err
	}
	defer comRelease(objArray)

	for i := uint32(0); i < count; i++ {
		var desktop unsafe.Pointer
		hr := comCall(objArray, slotObjectArrayGetAt,
			uintptr(i),
			uintptr(unsafe.Pointer(sw.iidVirtualDesktop)),
			uintptr(unsafe.Pointer(&desktop)),
		)
		if hrFailed(hr) || desktop == nil {
			return 0, fmt.Errorf("IObjectArray::GetAt(%d): hr=0x%08X", i, uint32(hr))
		}
		id, err := desktopID(desktop)
		comRelease(desktop)
		if err != nil {
			return 0, err
		}
		if id == currentID {
			return int(i), nil
		}
	}
	return 0, fmt.Errorf("current desktop not found among %d desktops", count)
}

// SwitchTo switches to the desktop at the given zero-based index. If no
// such desktop exists, it does nothing.
func (sw *Switcher) SwitchTo(zeroBasedIndex int) error {
	provider, err := coCreateInstance(clsidImmersiveShell, clsctxLocalServer, iidIServiceProvider)
	if err != nil {
		return fmt.Errorf("create ImmersiveShell instance: %w", err)
	}
	defer comRelease(provider)

	managerInternal, err := sw.queryManagerInternal(provider)
	if err != nil {
		return err
	}
	defer comRelease(managerInternal)

	desktop, err := sw.desktopAt(managerInternal, zeroBasedIndex)
	if err != nil || desktop == nil {
		return err
	}
	defer comRelease(desktop)

	if hr := comCall(managerInternal, slotManagerInternalSwitchDesktop, uintptr(desktop)); hrFailed(hr) {
		return fmt.Errorf("switch_desktop: hr=0x%08X", uint32(hr))
	}
	return nil
}

// queryViewCollection fetches IApplicationViewCollection through the
// ImmersiveShell's IServiceProvider, using its own IID as both the
// "service" identifier and the requested interface (the pattern this one
// particular service uses).
func queryViewCollection(provider unsafe.Pointer) (unsafe.Pointer, error) {
	var viewCollection unsafe.Pointer
	hr := comCall(provider, slotServiceProviderQueryService,
		uintptr(unsafe.Pointer(iidIApplicationViewCollection)),
		uintptr(unsafe.Pointer(iidIApplicationViewCollection)),
		uintptr(unsafe.Pointer(&viewCollection)),
	)
	if hrFailed(hr) || viewCollection == nil {
		return nil, fmt.Errorf("query ApplicationViewCollection service: hr=0x%08X", uint32(hr))
	}
	return viewCollection, nil
}

// viewForHwnd resolves a top-level window to its IApplicationView (the
// caller must Release it).
func viewForHwnd(viewCollection unsafe.Pointer, hwnd uintptr) (unsafe.Pointer, error) {
	var view unsafe.Pointer
	hr := comCall(viewCollection, slotAppViewCollectionGetViewForHwnd, hwnd, uintptr(unsafe.Pointer(&view)))
	if hrFailed(hr) || view == nil {
		return nil, fmt.Errorf("IApplicationViewCollection::GetViewForHwnd: hr=0x%08X", uint32(hr))
	}
	return view, nil
}

// MoveWindowTo moves hwnd to the desktop at the given zero-based index,
// without switching to it, and reports whether it did. If no such desktop
// exists, or hwnd is 0, it does nothing.
//
// This deliberately does NOT use the public, documented
// IVirtualDesktopManager::MoveWindowToDesktop: in practice it reliably
// fails with E_ACCESSDENIED (0x80070005) for windows outside the calling
// process, which is a widely-reported limitation of that API, not
// something specific to this app. IVirtualDesktopManagerInternal's
// undocumented move_view_to_desktop, operating on an IApplicationView
// instead of a raw HWND, is what actually works - the same approach real
// tools (e.g. VirtualDesktopAccessor) use for exactly this reason.
func (sw *Switcher) MoveWindowTo(hwnd uintptr, zeroBasedIndex int) (bool, error) {
	if hwnd == 0 {
		return false, nil
	}

	provider, err := coCreateInstance(clsidImmersiveShell, clsctxLocalServer, iidIServiceProvider)
	if err != nil {
		return false, fmt.Errorf("create ImmersiveShell instance: %w", err)
	}
	defer comRelease(provider)

	managerInternal, err := sw.queryManagerInternal(provider)
	if err != nil {
		return false, err
	}
	defer comRelease(managerInternal)

	desktop, err := sw.desktopAt(managerInternal, zeroBasedIndex)
	if err != nil || desktop == nil {
		return false, err
	}
	defer comRelease(desktop)

	viewCollection, err := queryViewCollection(provider)
	if err != nil {
		return false, err
	}
	defer comRelease(viewCollection)

	view, err := viewForHwnd(viewCollection, hwnd)
	if err != nil {
		return false, err
	}
	defer comRelease(view)

	if hr := comCall(managerInternal, slotManagerInternalMoveViewToDesktop, uintptr(view), uintptr(desktop)); hrFailed(hr) {
		return false, fmt.Errorf("move_view_to_desktop: hr=0x%08X", uint32(hr))
	}
	return true, nil
}
