//go:build windows

// Package singleinstance guarantees at most one running copy of a program
// per user session via a named Win32 mutex - independent of, and a
// belt-and-suspenders complement to, the installer's own
// terminate-before-replace logic.
package singleinstance

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Acquire tries to become the sole running instance identified by name (a
// process-unique string; it is namespaced as a Local\ kernel object, so it
// only guards against other instances in the same login session, which is
// what "don't run twice for this user" needs).
//
// If another instance already holds it, alreadyRunning is true and release
// is nil. Otherwise release must be called (typically via defer) to give up
// the lock before the process exits; letting the process die without
// calling it also releases the mutex, since Windows abandons mutexes held
// by a terminated process.
func Acquire(name string) (release func(), alreadyRunning bool, err error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, true, namePtr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// CreateMutex still hands back a valid handle to the existing
		// mutex in this case; it's ours to close.
		windows.CloseHandle(h)
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	release = func() {
		windows.ReleaseMutex(h)
		windows.CloseHandle(h)
	}
	return release, false, nil
}
