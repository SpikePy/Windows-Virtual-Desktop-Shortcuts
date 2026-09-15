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
)

// vtable slot indices (0-based, counting QueryInterface=0, AddRef=1,
// Release=2 as the first three slots shared by every COM interface).
const (
	slotServiceProviderQueryService = 3

	slotManagerInternalGetDesktopCount = 3
	slotManagerInternalGetDesktops     = 7
	slotManagerInternalSwitchDesktop   = 9

	slotObjectArrayGetCount = 3
	slotObjectArrayGetAt    = 4
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

// runDesktopSwitcher processes desktop-switch requests until the channel is
// closed. It must be started via `go runDesktopSwitcher(...)` from a
// goroutine that is allowed to own an OS thread indefinitely.
func runDesktopSwitcher(requests <-chan int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		messageBoxError(fmt.Sprintf("Failed to initialize COM: %v", err), "Virtual Desktop Switcher")
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

	for zeroBasedIndex := range requests {
		_ = sw.switchTo(zeroBasedIndex)
	}
}

// switchTo switches to the desktop at the given zero-based index. If no
// such desktop exists, it does nothing (returns nil).
func (sw *desktopSwitcher) switchTo(zeroBasedIndex int) error {
	provider, err := coCreateInstance(clsidImmersiveShell, clsctxLocalServer, iidIServiceProvider)
	if err != nil {
		return fmt.Errorf("create ImmersiveShell instance: %w", err)
	}
	defer comRelease(provider)

	var managerInternal unsafe.Pointer
	hr := comCall(provider, slotServiceProviderQueryService,
		uintptr(unsafe.Pointer(clsidVirtualDesktopManagerInternal)),
		uintptr(unsafe.Pointer(sw.iidManagerInternal)),
		uintptr(unsafe.Pointer(&managerInternal)),
	)
	if hrFailed(hr) || managerInternal == nil {
		return fmt.Errorf("query VirtualDesktopManagerInternal service: hr=0x%08X", uint32(hr))
	}
	defer comRelease(managerInternal)

	var objArray unsafe.Pointer
	hr = comCall(managerInternal, slotManagerInternalGetDesktops, uintptr(unsafe.Pointer(&objArray)))
	if hrFailed(hr) || objArray == nil {
		return fmt.Errorf("get_desktops: hr=0x%08X", uint32(hr))
	}
	defer comRelease(objArray)

	var count uint32
	hr = comCall(objArray, slotObjectArrayGetCount, uintptr(unsafe.Pointer(&count)))
	if hrFailed(hr) {
		return fmt.Errorf("IObjectArray::GetCount: hr=0x%08X", uint32(hr))
	}

	if uint32(zeroBasedIndex) >= count {
		// Desktop N doesn't exist; silently ignore per spec.
		return nil
	}

	var desktop unsafe.Pointer
	hr = comCall(objArray, slotObjectArrayGetAt,
		uintptr(uint32(zeroBasedIndex)),
		uintptr(unsafe.Pointer(sw.iidVirtualDesktop)),
		uintptr(unsafe.Pointer(&desktop)),
	)
	if hrFailed(hr) || desktop == nil {
		return fmt.Errorf("IObjectArray::GetAt(%d): hr=0x%08X", zeroBasedIndex, uint32(hr))
	}
	defer comRelease(desktop)

	hr = comCall(managerInternal, slotManagerInternalSwitchDesktop, uintptr(desktop))
	if hrFailed(hr) {
		return fmt.Errorf("switch_desktop: hr=0x%08X", uint32(hr))
	}
	return nil
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
