//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/setup"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/tray"
)

// Setup's window is a Windows task dialog (TaskDialogIndirect) with three
// pages: the choice (Install/Update, Uninstall, Close, plus a countdown
// that installs on its own), progress (marquee bar, current step), and
// the result (with a countdown that closes it after a success).
//
// Each page is a *page passed as the dialog's lpCallbackData, so the
// callback always knows which page it is serving. The callback runs on
// the dialog's own thread; the install/uninstall runs on a worker
// goroutine that only talks to the dialog through SendMessage.

var (
	// Loaded by bare name, not from System32, so the manifest's Common
	// Controls v6 dependency can redirect it - TaskDialogIndirect only
	// exists in v6.
	procTaskDialogIndirect = windows.NewLazyDLL("comctl32.dll").NewProc("TaskDialogIndirect")

	modUser32                  = windows.NewLazySystemDLL("user32.dll")
	procSendMessageW           = modUser32.NewProc("SendMessageW")
	procPostMessageW           = modUser32.NewProc("PostMessageW")
	procGetDpiForSystem        = modUser32.NewProc("GetDpiForSystem")
	procGetSystemMetricsForDpi = modUser32.NewProc("GetSystemMetricsForDpi")
)

const (
	tdfUseHIconMain       = 0x0002
	tdfShowMarqueeBar     = 0x0400
	tdfCallbackTimer      = 0x0800
	tdnCreated            = 0
	tdnNavigated          = 1
	tdnButtonClicked      = 2
	tdnTimer              = 4
	tdnRadioButtonClicked = 6

	wmUser                    = 0x0400
	tdmNavigatePage           = wmUser + 101
	tdmClickButton            = wmUser + 102
	tdmSetMarquee             = wmUser + 107
	tdmSetElementText         = wmUser + 108
	tdmEnableButton           = wmUser + 111
	tdeContent                = 0
	tdErrorIcon       uintptr = 0xFFFE // MAKEINTRESOURCE(-2)
	smCxIcon                  = 11

	sOK    = 0
	sFalse = 1

	idCancel    = 2 // Close: Escape and the title-bar X send it too
	idInstall   = 100
	idUninstall = 101

	dialogTitle = "Virtual Desktop Shortcuts Setup"
	whatItDoes  = "Virtual Desktop Shortcuts switches virtual desktops with Ctrl+Alt+1-9, " +
		"Ctrl+Alt+arrows or Ctrl+Alt+mouse wheel, and moves windows between them with Shift."
	chooseHint = "Choose Install/Update or Uninstall."
)

// errNoDialog marks a failure to show the dialog at all, as opposed to a
// failed install or uninstall, which the dialog reports itself.
var errNoDialog = errors.New("couldn't open the Setup window")

type pageKind int

const (
	pageChoose pageKind = iota
	pageProgress
	pageResult
)

// shared is what every page needs and nothing ever changes after the
// dialog starts, so the worker goroutine can read it too.
type shared struct {
	version string
	opts    setup.Options
	icon    uintptr
	results chan<- error    // the action's outcome, for the exit code
	quit    <-chan struct{} // closed once the dialog is gone
}

// page is one task dialog page. Apart from the immutable *shared, its
// fields are only touched on the dialog's thread.
type page struct {
	*shared
	kind    pageKind
	install bool   // progress page: install (true) or uninstall
	ok      bool   // result page: the action succeeded
	text    string // result page: the message above the countdown

	counting   bool
	resetTimer bool
	shown      string // the content text last set by setContent
	next       *page  // the progress page, kept alive while it's shown

	cfg  []byte
	keep []any // strings and buttons the config points at
}

// callback is dialogCallback as a function pointer for the dialog. It's
// set in init because build refers to it, which would otherwise make an
// initialization cycle.
var callback uintptr

func init() { callback = syscall.NewCallback(dialogCallback) }

// runDialog shows Setup's dialog and returns once it is closed, with the
// error of the action that ran (nil if it succeeded or nothing ran).
func runDialog(version string, opts setup.Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := procTaskDialogIndirect.Find(); err != nil {
		return fmt.Errorf("%w: %v", errNoDialog, err)
	}
	dpi, _, _ := procGetDpiForSystem.Call()
	size, _, _ := procGetSystemMetricsForDpi.Call(smCxIcon, dpi)
	icon, err := tray.AppIcon(int(size))
	if err != nil {
		return fmt.Errorf("%w: %v", errNoDialog, err)
	}
	defer tray.DestroyIconHandle(icon)

	results := make(chan error, 1)
	quit := make(chan struct{})
	defer close(quit)

	first := &page{
		shared: &shared{version: version, opts: opts, icon: icon, results: results, quit: quit},
		kind:   pageChoose,
	}
	first.build("Install or update Virtual Desktop Shortcuts?",
		whatItDoes+"\n\n"+setup.InstallCountdownText(int(setup.AutoInstallAfter/time.Second)),
		[]button{{idInstall, "Install/Update"}, {idUninstall, "Uninstall"}, {idCancel, "Close"}},
		idInstall, false)

	hr, _, _ := procTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&first.cfg[0])), 0, 0, 0)
	runtime.KeepAlive(first)
	if hr != 0 {
		return fmt.Errorf("%w: TaskDialogIndirect returned 0x%08X", errNoDialog, hr)
	}
	select {
	case err := <-results:
		return err
	default:
		return nil
	}
}

type button struct {
	id   int32
	text string
}

// build fills p.cfg with a TASKDIALOGCONFIG. The struct is 1-byte packed
// (160 bytes on x64), so it's written at fixed offsets rather than
// declared as a Go struct.
func (p *page) build(instruction, content string, buttons []button, defaultButton int32, errorIcon bool) {
	cfg := make([]byte, 160)
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(cfg[off:], v) }
	putPtr := func(off int, v uintptr) { binary.LittleEndian.PutUint64(cfg[off:], uint64(v)) }

	flags := uint32(tdfCallbackTimer)
	if p.kind == pageProgress {
		flags |= tdfShowMarqueeBar
	}
	if errorIcon {
		putPtr(36, tdErrorIcon)
	} else {
		flags |= tdfUseHIconMain
		putPtr(36, p.icon)
	}

	btns := make([]byte, 12*len(buttons)) // TASKDIALOG_BUTTON, also packed
	for i, b := range buttons {
		binary.LittleEndian.PutUint32(btns[12*i:], uint32(b.id))
		binary.LittleEndian.PutUint64(btns[12*i+4:], uint64(p.str(b.text)))
	}
	p.keep = append(p.keep, btns)

	put32(0, uint32(len(cfg)))
	put32(20, flags)
	putPtr(28, p.str(dialogTitle))
	putPtr(44, p.str(instruction))
	putPtr(52, p.str(content))
	put32(60, uint32(len(buttons)))
	putPtr(64, uintptr(unsafe.Pointer(&btns[0])))
	put32(72, uint32(defaultButton))
	putPtr(132, p.str(fmt.Sprintf("Setup %s - installs for your account only, no administrator rights needed.", p.version)))
	putPtr(140, callback)
	putPtr(148, uintptr(unsafe.Pointer(p)))
	p.cfg = cfg
}

// str returns a UTF-16 copy of s that stays alive as long as p does.
func (p *page) str(s string) uintptr {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		u, _ = windows.UTF16FromString("?")
	}
	p.keep = append(p.keep, u)
	return uintptr(unsafe.Pointer(&u[0]))
}

func dialogCallback(hwnd, msg, wParam, lParam, ref uintptr) uintptr {
	p := (*page)(unsafe.Pointer(ref))
	switch msg {
	case tdnCreated, tdnNavigated:
		p.enter(hwnd)
	case tdnTimer:
		return p.tick(hwnd, time.Duration(wParam)*time.Millisecond)
	case tdnButtonClicked:
		return p.clicked(hwnd, int32(wParam))
	case tdnRadioButtonClicked:
		p.stopCountdown(hwnd)
	}
	return sOK
}

// enter runs when p becomes the page on screen.
func (p *page) enter(hwnd uintptr) {
	switch p.kind {
	case pageChoose:
		p.counting, p.resetTimer = true, true
	case pageProgress:
		procSendMessageW.Call(hwnd, tdmSetMarquee, 1, 0)
		procSendMessageW.Call(hwnd, tdmEnableButton, idCancel, 0)
		go work(hwnd, p.shared, p.install)
	case pageResult:
		p.counting, p.resetTimer = p.ok, true
	}
}

// tick advances the page's countdown. The first tick on a page returns
// S_FALSE, which restarts the dialog's tick count, so elapsed is measured
// from when the page appeared.
func (p *page) tick(hwnd uintptr, elapsed time.Duration) uintptr {
	if p.resetTimer {
		p.resetTimer = false
		return sFalse
	}
	if !p.counting {
		return sOK
	}
	total, id, text := setup.AutoInstallAfter, idInstall, setup.InstallCountdownText
	if p.kind == pageResult {
		total, id, text = setup.AutoCloseAfter, idCancel, setup.CloseCountdownText
	}
	seconds, done := setup.Remaining(total, elapsed)
	if done {
		p.counting = false
		procPostMessageW.Call(hwnd, tdmClickButton, uintptr(id), 0)
		return sOK
	}
	if p.kind == pageResult {
		p.setContent(hwnd, p.text+"\n\n"+text(seconds))
	} else {
		p.setContent(hwnd, whatItDoes+"\n\n"+text(seconds))
	}
	return sOK
}

// stopCountdown cancels page one's countdown for good once the user has
// interacted with the dialog.
func (p *page) stopCountdown(hwnd uintptr) {
	if p.kind == pageChoose && p.counting {
		p.counting = false
		p.setContent(hwnd, whatItDoes+"\n\n"+chooseHint)
	}
}

func (p *page) setContent(hwnd uintptr, text string) {
	if text == p.shown {
		return
	}
	p.shown = text
	procSendMessageW.Call(hwnd, tdmSetElementText, tdeContent, p.str(text))
}

// clicked handles a button. Returning S_FALSE keeps the dialog open.
func (p *page) clicked(hwnd uintptr, id int32) uintptr {
	switch p.kind {
	case pageChoose:
		if id != idInstall && id != idUninstall {
			return sOK // Close
		}
		p.counting = false
		p.next = &page{shared: p.shared, kind: pageProgress, install: id == idInstall}
		title := "Uninstalling..."
		if p.next.install {
			title = "Installing/updating..."
		}
		p.next.build(title, "Starting...", []button{{idCancel, "Close"}}, idCancel, false)
		procSendMessageW.Call(hwnd, tdmNavigatePage, 0, uintptr(unsafe.Pointer(&p.next.cfg[0])))
		return sFalse
	case pageProgress:
		return sFalse // Close is disabled until the action has finished
	}
	return sOK
}

// work runs the install or uninstall on its own goroutine, reports each
// step into the progress page and then navigates to the result page. It
// keeps everything it handed to the dialog alive until the dialog is
// gone.
func work(hwnd uintptr, s *shared, install bool) {
	var steps [][]uint16
	opts := s.opts
	opts.Progress = func(text string) {
		u, err := windows.UTF16FromString(text)
		if err != nil {
			return
		}
		steps = append(steps, u)
		procSendMessageW.Call(hwnd, tdmSetElementText, tdeContent, uintptr(unsafe.Pointer(&u[0])))
	}

	var instruction, text string
	var err error
	if install {
		var tag string
		tag, err = setup.Install(opts)
		instruction = fmt.Sprintf("Virtual Desktop Shortcuts %s is installed", tag)
		text = "It's running now - look for its icon in the system tray. Right-click it for settings."
		if opts.NoLaunch {
			text = "It will start the next time you sign in to Windows."
		}
		if err != nil {
			instruction = "Installing/updating failed"
		}
	} else {
		err = setup.Uninstall(opts)
		instruction = "Virtual Desktop Shortcuts is uninstalled"
		text = "The app, its settings and its Startup shortcut have been removed."
		if err != nil {
			instruction = "Uninstalling failed"
		}
	}
	if err != nil {
		text = err.Error() + "\n\nRunning Setup again is safe."
	}
	s.results <- err

	result := &page{shared: s, kind: pageResult, ok: err == nil, text: text}
	content := text
	if result.ok {
		content += "\n\n" + setup.CloseCountdownText(int(setup.AutoCloseAfter/time.Second))
	}
	result.build(instruction, content, []button{{idCancel, "Close"}}, idCancel, !result.ok)
	procSendMessageW.Call(hwnd, tdmNavigatePage, 0, uintptr(unsafe.Pointer(&result.cfg[0])))

	<-s.quit
	runtime.KeepAlive(result)
	runtime.KeepAlive(steps)
}
