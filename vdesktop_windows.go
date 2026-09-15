//go:build windows

package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Virtual Desktop switching functionality is not part of any public,
// documented Windows API. It relies on undocumented COM interfaces exposed
// by explorer.exe that Microsoft can (and periodically does) change between
// releases. The interface IDs below differ between the "Windows 10 era"
// (builds 10240-21999) and the "Windows 11 era" (builds 22000+), but the
// vtable method order has stayed stable across both eras, which is what
// this code relies on.
//
// Sources cross-checked against the (differently-licensed, not vendored
// here) Ciantic/VirtualDesktopAccessor project, which maintains the most
// complete public record of these interfaces:
//   https://github.com/Ciantic/VirtualDesktopAccessor

var (
	clsidImmersiveShell                = guid("{C2F03A33-21F5-47FA-B4BB-156362A2F239}")
	clsidVirtualDesktopManagerInternal = guid("{C5E0CDCA-7B6E-41B2-9FC4-D93975CC467B}")
	iidIServiceProvider                = guid("{6D5140C1-7436-11CE-8034-00AA006009FA}")
	iidIObjectArray                    = guid("{92CA9DCD-5622-4BBA-A805-5E9F541BD8C9}")

	// Windows 10 (builds 10240-21999).
	iidVirtualDesktopManagerInternalWin10 = guid("{0F3A72B0-4566-487E-9A33-4ED302F6D6CE}")
	iidVirtualDesktopWin10                = guid("{FF72FFDD-BE7E-43FC-9C03-AD81681E88E4}")

	// Windows 11 (builds 22000+).
	iidVirtualDesktopManagerInternalWin11 = guid("{53F5CA0B-158F-4124-900C-057158060B27}")
	iidVirtualDesktopWin11                = guid("{3F07F4BE-B107-441A-AF0F-39D82529072C}")

	// IApplicationViewCollection is what turns a plain HWND into the
	// IApplicationView that move_view_to_desktop (below) needs. Same IID
	// on every Windows version, and -- unusually for this file -- it's
	// queried from IServiceProvider using its own IID as both the
	// "service" and the requested interface.
	iidIApplicationViewCollection = guid("{1841C6D7-4F9D-42C0-AF41-8747538F10E5}")
)

// vtable slot indices (0-based, counting QueryInterface=0, AddRef=1,
// Release=2 as the first three slots shared by every COM interface).
const (
	slotServiceProviderQueryService = 3

	slotManagerInternalGetDesktopCount   = 3
	slotManagerInternalMoveViewToDesktop = 4
	slotManagerInternalGetCurrentDesktop = 6
	slotManagerInternalGetDesktops       = 7
	slotManagerInternalSwitchDesktop     = 9

	slotVirtualDesktopGetID = 4

	slotObjectArrayGetCount = 3
	slotObjectArrayGetAt    = 4

	slotAppViewCollectionGetViewForHwnd = 6
)

func guid(s string) *windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(fmt.Sprintf("invalid GUID %q: %v", s, err))
	}
	return &g
}

// desktopSwitcher owns the COM apartment used to talk to the virtual
// desktop manager. It must live on a single, dedicated OS thread for the
// lifetime of the apartment (COINIT_APARTMENTTHREADED objects are not
// thread-safe and must be used from the thread that created them).
type desktopSwitcher struct {
	iidManagerInternal *windows.GUID
	iidVirtualDesktop  *windows.GUID
}

// desktopAction identifies what a hotkey-triggered request should do.
type desktopAction int

const (
	actionSwitchToDesktop     desktopAction = iota // Win+N, Win+Left/Right
	actionMoveWindowToDesktop                      // Win+Shift+N, Win+Shift+Left/Right
)

type desktopRequest struct {
	action desktopAction
	// index is the 0-based target desktop or, with relative set, an offset
	// from the current desktop (-1 previous, +1 next).
	index    int
	relative bool
}

// runDesktopSwitcher processes desktop switch/move requests until the
// channel is closed. It must be started via `go runDesktopSwitcher(...)`
// from a goroutine that is allowed to own an OS thread indefinitely.
func runDesktopSwitcher(requests <-chan desktopRequest) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		messageBoxError(fmt.Sprintf("Failed to initialize COM: %v", err), appName)
		return
	}
	defer windows.CoUninitialize()

	ver := windows.RtlGetVersion()
	sw := &desktopSwitcher{
		iidManagerInternal: iidVirtualDesktopManagerInternalWin10,
		iidVirtualDesktop:  iidVirtualDesktopWin10,
	}
	if ver.BuildNumber >= 22000 {
		sw.iidManagerInternal = iidVirtualDesktopManagerInternalWin11
		sw.iidVirtualDesktop = iidVirtualDesktopWin11
	}

	for req := range requests {
		if err := sw.handle(req); err != nil {
			// This app has no console/UI for routine errors; surface them
			// via OutputDebugString (visible in DebugView or a debugger)
			// instead of silently discarding them.
			debugLogf("request %+v failed: %v", req, err)
		}
	}
}

// handle resolves a request's target desktop and carries it out. Relative
// requests past the first or last desktop do nothing.
//
// Before acting, it takes focus away from whatever app has it by focusing
// the desktop, after noting the focused window for the move actions.
func (sw *desktopSwitcher) handle(req desktopRequest) error {
	desktop := desktopWindow()
	hwnd := getForegroundWindow()
	if hwnd == desktop {
		hwnd = 0
	}
	if !activateWindow(desktop) {
		debugLogf("handle: couldn't move focus to the desktop")
	}

	index := req.index
	if req.relative {
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
	case req.action == actionSwitchToDesktop:
		return sw.switchTo(index)
	case !req.relative:
		_, err := sw.moveWindowTo(hwnd, index)
		return err
	default:
		// Win+Shift+Left/Right takes the window along and refocuses it,
		// so pressing it again keeps moving the same window.
		moved, err := sw.moveWindowTo(hwnd, index)
		if err != nil || !moved {
			return err
		}
		if err := sw.switchTo(index); err != nil {
			return err
		}
		if !activateWindow(hwnd) {
			debugLogf("handle: couldn't refocus the moved window")
		}
		return nil
	}
}

// queryManagerInternal fetches the undocumented VirtualDesktopManagerInternal
// service through the ImmersiveShell's IServiceProvider. Every action in
// this file starts here.
func (sw *desktopSwitcher) queryManagerInternal(provider unsafe.Pointer) (unsafe.Pointer, error) {
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
// zero-based index (the caller must Release it), or nil if no such
// desktop exists.
func (sw *desktopSwitcher) desktopAt(managerInternal unsafe.Pointer, zeroBasedIndex int) (unsafe.Pointer, error) {
	var objArray unsafe.Pointer
	hr := comCall(managerInternal, slotManagerInternalGetDesktops, uintptr(unsafe.Pointer(&objArray)))
	if hrFailed(hr) || objArray == nil {
		return nil, fmt.Errorf("get_desktops: hr=0x%08X", uint32(hr))
	}
	defer comRelease(objArray)

	var count uint32
	hr = comCall(objArray, slotObjectArrayGetCount, uintptr(unsafe.Pointer(&count)))
	if hrFailed(hr) {
		return nil, fmt.Errorf("IObjectArray::GetCount: hr=0x%08X", uint32(hr))
	}

	if uint32(zeroBasedIndex) >= count {
		// Desktop N doesn't exist.
		return nil, nil
	}

	var desktop unsafe.Pointer
	hr = comCall(objArray, slotObjectArrayGetAt,
		uintptr(uint32(zeroBasedIndex)),
		uintptr(unsafe.Pointer(sw.iidVirtualDesktop)),
		uintptr(unsafe.Pointer(&desktop)),
	)
	if hrFailed(hr) || desktop == nil {
		return nil, fmt.Errorf("IObjectArray::GetAt(%d): hr=0x%08X", zeroBasedIndex, uint32(hr))
	}
	return desktop, nil
}

// desktopID returns the GUID identifying an IVirtualDesktop.
func desktopID(desktop unsafe.Pointer) (windows.GUID, error) {
	var id windows.GUID
	hr := comCall(desktop, slotVirtualDesktopGetID, uintptr(unsafe.Pointer(&id)))
	if hrFailed(hr) {
		return windows.GUID{}, fmt.Errorf("IVirtualDesktop::GetID: hr=0x%08X", uint32(hr))
	}
	return id, nil
}

// currentDesktopID returns the GUID of the desktop currently shown.
func currentDesktopID(managerInternal unsafe.Pointer) (windows.GUID, error) {
	var desktop unsafe.Pointer
	hr := comCall(managerInternal, slotManagerInternalGetCurrentDesktop, uintptr(unsafe.Pointer(&desktop)))
	if hrFailed(hr) || desktop == nil {
		return windows.GUID{}, fmt.Errorf("get_current_desktop: hr=0x%08X", uint32(hr))
	}
	defer comRelease(desktop)
	return desktopID(desktop)
}

// currentDesktopIndex returns the zero-based index of the desktop
// currently shown.
func (sw *desktopSwitcher) currentDesktopIndex() (int, error) {
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

	currentID, err := currentDesktopID(managerInternal)
	if err != nil {
		return 0, err
	}

	var objArray unsafe.Pointer
	hr := comCall(managerInternal, slotManagerInternalGetDesktops, uintptr(unsafe.Pointer(&objArray)))
	if hrFailed(hr) || objArray == nil {
		return 0, fmt.Errorf("get_desktops: hr=0x%08X", uint32(hr))
	}
	defer comRelease(objArray)

	var count uint32
	hr = comCall(objArray, slotObjectArrayGetCount, uintptr(unsafe.Pointer(&count)))
	if hrFailed(hr) {
		return 0, fmt.Errorf("IObjectArray::GetCount: hr=0x%08X", uint32(hr))
	}
	for i := uint32(0); i < count; i++ {
		var desktop unsafe.Pointer
		hr = comCall(objArray, slotObjectArrayGetAt,
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

// switchTo switches to the desktop at the given zero-based index. If no
// such desktop exists, or it's already the current one, it does nothing.
func (sw *desktopSwitcher) switchTo(zeroBasedIndex int) error {
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
	if err != nil {
		return err
	}
	if desktop == nil {
		return nil
	}
	defer comRelease(desktop)

	targetID, err := desktopID(desktop)
	if err != nil {
		return err
	}
	currentID, err := currentDesktopID(managerInternal)
	if err != nil {
		return err
	}
	debugLogf("switchTo(%d): on %s, target %s", zeroBasedIndex+1, shortID(currentID), shortID(targetID))
	if currentID == targetID {
		return nil
	}

	hr := comCall(managerInternal, slotManagerInternalSwitchDesktop, uintptr(desktop))
	if hrFailed(hr) {
		return fmt.Errorf("switch_desktop: hr=0x%08X", uint32(hr))
	}
	return nil
}

// shortID abbreviates a desktop GUID for log output.
func shortID(id windows.GUID) string {
	return fmt.Sprintf("%08X", id.Data1)
}

// queryViewCollection fetches IApplicationViewCollection through the
// ImmersiveShell's IServiceProvider, using its own IID as both the
// "service" identifier and the requested interface (the pattern this one
// particular service uses).
func (sw *desktopSwitcher) queryViewCollection(provider unsafe.Pointer) (unsafe.Pointer, error) {
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

// moveWindowTo moves hwnd to the desktop at the given zero-based index,
// without switching to it, and reports whether it did. If no such desktop
// exists, or hwnd is 0, it does nothing.
//
// This deliberately does NOT use the public, documented
// IVirtualDesktopManager::MoveWindowToDesktop: in practice it reliably
// fails with E_ACCESSDENIED (0x80070005) for windows outside the calling
// process, which is a widely-reported limitation of that API, not
// something specific to this app. IVirtualDesktopManagerInternal's
// undocumented move_view_to_desktop, operating on an IApplicationView
// instead of a raw HWND, is what actually works -- the same approach real
// tools (e.g. VirtualDesktopAccessor) use for exactly this reason.
func (sw *desktopSwitcher) moveWindowTo(hwnd uintptr, zeroBasedIndex int) (bool, error) {
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
	if err != nil {
		return false, err
	}
	if desktop == nil {
		return false, nil
	}
	defer comRelease(desktop)

	viewCollection, err := sw.queryViewCollection(provider)
	if err != nil {
		return false, err
	}
	defer comRelease(viewCollection)

	view, err := viewForHwnd(viewCollection, hwnd)
	if err != nil {
		return false, err
	}
	defer comRelease(view)

	hr := comCall(managerInternal, slotManagerInternalMoveViewToDesktop, uintptr(view), uintptr(desktop))
	if hrFailed(hr) {
		return false, fmt.Errorf("move_view_to_desktop: hr=0x%08X", uint32(hr))
	}
	return true, nil
}

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
