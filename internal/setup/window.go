//go:build windows

package setup

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/setupmenu"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/tray"
	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/win32"
)

// The Setup window is one small, fixed-size dialog built from plain Win32
// controls: the app's icon, name and version, a line saying what will
// happen, Install/update (the default, counting down) and Uninstall, then
// a status line and a Close button. Visual styles and per-monitor DPI
// awareness come from the manifest embedded in the exe's .syso.

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")
	modGdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procCreateWindowExW            = modUser32.NewProc("CreateWindowExW")
	procSetWindowTextW             = modUser32.NewProc("SetWindowTextW")
	procShowWindow                 = modUser32.NewProc("ShowWindow")
	procEnableWindow               = modUser32.NewProc("EnableWindow")
	procSetFocus                   = modUser32.NewProc("SetFocus")
	procSendMessageW               = modUser32.NewProc("SendMessageW")
	procSetTimer                   = modUser32.NewProc("SetTimer")
	procKillTimer                  = modUser32.NewProc("KillTimer")
	procSetWindowPos               = modUser32.NewProc("SetWindowPos")
	procGetDpiForWindow            = modUser32.NewProc("GetDpiForWindow")
	procAdjustWindowRectExForDpi   = modUser32.NewProc("AdjustWindowRectExForDpi")
	procSystemParametersInfoForDpi = modUser32.NewProc("SystemParametersInfoForDpi")
	procMonitorFromWindow          = modUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW            = modUser32.NewProc("GetMonitorInfoW")
	procGetSysColor                = modUser32.NewProc("GetSysColor")
	procGetSysColorBrush           = modUser32.NewProc("GetSysColorBrush")
	procIsDialogMessageW           = modUser32.NewProc("IsDialogMessageW")
	procGetMessageW                = modUser32.NewProc("GetMessageW")
	procTranslateMessage           = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW           = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage            = modUser32.NewProc("PostQuitMessage")

	procCreateFontIndirectW = modGdi32.NewProc("CreateFontIndirectW")
	procDeleteObject        = modGdi32.NewProc("DeleteObject")
	procSetBkColor          = modGdi32.NewProc("SetBkColor")
)

const (
	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	wsExCtrlParent = 0x00010000

	bsPushButton    = 0x0
	bsDefPushButton = 0x1
	ssNoPrefix      = 0x80
	ssIcon          = 0x3
	ssEditControl   = 0x2000

	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmSetFont          = 0x0030
	wmSetIcon          = 0x0080
	wmKeyDown          = 0x0100
	wmSysKeyDown       = 0x0104
	wmCommand          = 0x0111
	wmTimer            = 0x0113
	wmCtlColorBtn      = 0x0135
	wmCtlColorStatic   = 0x0138
	wmLButtonDown      = 0x0201
	wmRButtonDown      = 0x0204
	wmNCLButtonDown    = 0x00A1
	wmDpiChanged       = 0x02E0
	bmSetStyle         = 0x00F4
	stmSetIcon         = 0x0170
	spiGetNonClientMet = 0x0029

	swHide = 0
	swShow = 5

	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	colorWindow = 5

	monitorDefaultToNearest = 2

	idOK        = 1 // Enter, via IsDialogMessage
	idCancel    = 2 // Escape, via IsDialogMessage
	idUninstall = 3
	idClose     = 4

	timerCountdown = 1
	timerAutoClose = 2

	// Private messages the worker goroutine posts back to the window.
	wmProgress = 0x8000 + 10
	wmDone     = 0x8000 + 11

	appTitle = "Virtual Desktop Shortcuts"
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	cbSize  uint32
	monitor rect
	work    rect
	flags   uint32
}

type logFont struct {
	Height, Width, Escapement, Orientation, Weight    int32
	Italic, Underline, StrikeOut, CharSet             byte
	OutPrecision, ClipPrecision, Quality, PitchFamily byte
	FaceName                                          [32]uint16
}

type nonClientMetrics struct {
	cbSize                                 uint32
	borderWidth, scrollWidth, scrollHeight int32
	captionWidth, captionHeight            int32
	captionFont                            logFont
	smCaptionWidth, smCaptionHeight        int32
	smCaptionFont                          logFont
	menuWidth, menuHeight                  int32
	menuFont, statusFont, messageFont      logFont
	paddedBorderWidth                      int32
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// Layout, in pixels at 96 DPI.
const (
	clientW   = 420
	margin    = 16
	iconDIP   = 48
	gap       = 12
	lineH     = 20
	titleH    = 28
	introH    = 2 * lineH
	statusH   = 3 * lineH
	buttonH   = 28
	installW  = 160
	otherW    = 100
	textLeft  = margin + iconDIP + gap
	introTop  = margin + iconDIP + gap
	statusTop = introTop + introH + gap/2
	buttonTop = statusTop + statusH + gap
	clientH   = buttonTop + buttonH + margin
)

// window is the Setup window's state. Everything except the fields behind
// mu is only touched on the thread running the message loop.
type window struct {
	opts    Options
	version string

	hwnd                                 uintptr
	icon, title, subtitle, intro, status uintptr
	install, uninstall, closeBtn         uintptr
	font, titleFont, bigIcon, smallIcon  uintptr
	dpi                                  int32

	countdown *setupmenu.Timer
	running   bool
	finished  bool
	auto      bool
	err       error

	mu       sync.Mutex
	progress string
	tag      string
}

// ErrNoWindow wraps a failure to create the Setup window itself.
var ErrNoWindow = errors.New("couldn't open the Setup window")

// RunWindow shows the Setup window and returns once it is closed, with the
// error of the action that ran (nil if it succeeded or nothing ran).
func RunWindow(version string, opts Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := &window{opts: opts, version: version, countdown: setupmenu.NewTimer(setupmenu.Countdown)}
	if err := w.create(); err != nil {
		return fmt.Errorf("%w: %v", ErrNoWindow, err)
	}
	w.loop()
	w.freeResources()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *window) create() error {
	const class = "VirtualDesktopShortcutsSetup"
	brush, _, _ := procGetSysColorBrush.Call(colorWindow)
	if err := win32.RegisterClass(class, syscall.NewCallback(w.proc), syscall.Handle(brush)); err != nil {
		return err
	}

	var err error
	style := uint32(wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox)
	w.hwnd, err = win32.CreateWindow(wsExCtrlParent, style, class, appTitle+" Setup",
		win32.CWUseDefault, win32.CWUseDefault, clientW, clientH)
	if err != nil {
		return err
	}

	w.icon = w.child("STATIC", "", ssIcon, 0)
	w.title = w.child("STATIC", appTitle, ssNoPrefix, 0)
	w.subtitle = w.child("STATIC", "Setup "+w.version, ssNoPrefix, 0)
	w.intro = w.child("STATIC",
		"Installs or updates the app for your Windows account and starts it - no administrator rights needed. Uninstall removes it again.",
		ssNoPrefix, 0)
	w.status = w.child("STATIC", "", ssNoPrefix|ssEditControl, 0)
	w.install = w.child("BUTTON", w.countdown.Label("Install / update"), wsTabStop|bsDefPushButton, idOK)
	w.uninstall = w.child("BUTTON", "Uninstall", wsTabStop|bsPushButton, idUninstall)
	w.closeBtn = w.child("BUTTON", "Close", wsTabStop|bsDefPushButton, idClose)
	procShowWindow.Call(w.closeBtn, swHide)

	dpi, _, _ := procGetDpiForWindow.Call(w.hwnd)
	w.applyDPI(int32(dpi))
	w.centre()

	procShowWindow.Call(w.hwnd, swShow)
	win32.SetForegroundWindow(w.hwnd)
	procSetFocus.Call(w.install)
	procSetTimer.Call(w.hwnd, timerCountdown, 1000, 0)
	return nil
}

func (w *window) child(class, text string, style uintptr, id uintptr) uintptr {
	h, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(win32.UTF16Ptr(class))),
		uintptr(unsafe.Pointer(win32.UTF16Ptr(text))),
		wsChild|wsVisible|style, 0, 0, 0, 0, w.hwnd, id, uintptr(win32.ModuleHandle()), 0)
	return h
}

// scale converts a 96-DPI length to the window's current DPI.
func (w *window) scale(v int32) int32 { return v * w.dpi / 96 }

// applyDPI (re)creates the fonts and icons for dpi and lays the controls
// out again - on creation and whenever the window moves to a monitor with
// a different scale.
func (w *window) applyDPI(dpi int32) {
	w.dpi = dpi
	w.freeResources()

	var ncm nonClientMetrics
	ncm.cbSize = uint32(unsafe.Sizeof(ncm))
	procSystemParametersInfoForDpi.Call(spiGetNonClientMet, uintptr(ncm.cbSize), uintptr(unsafe.Pointer(&ncm)), 0, uintptr(dpi))
	w.font, _, _ = procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&ncm.messageFont)))
	big := ncm.messageFont
	big.Height = big.Height * 3 / 2
	big.Weight = 600 // semibold
	w.titleFont, _, _ = procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&big)))

	w.bigIcon, _ = tray.AppIcon(int(w.scale(iconDIP)))
	w.smallIcon, _ = tray.AppIcon(int(w.scale(16)))
	procSendMessageW.Call(w.icon, stmSetIcon, w.bigIcon, 0)
	procSendMessageW.Call(w.hwnd, wmSetIcon, 1, w.bigIcon)   // ICON_BIG
	procSendMessageW.Call(w.hwnd, wmSetIcon, 0, w.smallIcon) // ICON_SMALL

	for _, c := range []uintptr{w.subtitle, w.intro, w.status, w.install, w.uninstall, w.closeBtn} {
		procSendMessageW.Call(c, wmSetFont, w.font, 1)
	}
	procSendMessageW.Call(w.title, wmSetFont, w.titleFont, 1)

	textW := int32(clientW - textLeft - margin)
	fullW := int32(clientW - 2*margin)
	w.place(w.icon, margin, margin, iconDIP, iconDIP)
	w.place(w.title, textLeft, margin, textW, titleH)
	w.place(w.subtitle, textLeft, margin+titleH, textW, lineH)
	w.place(w.intro, margin, introTop, fullW, introH)
	w.place(w.status, margin, statusTop, fullW, statusH)
	right := int32(clientW - margin)
	w.place(w.closeBtn, right-otherW, buttonTop, otherW, buttonH)
	w.place(w.uninstall, right-otherW, buttonTop, otherW, buttonH)
	w.place(w.install, right-otherW-gap/2-installW, buttonTop, installW, buttonH)
}

// place positions a control, taking 96-DPI coordinates.
func (w *window) place(h uintptr, x, y, width, height int32) {
	procSetWindowPos.Call(h, 0, uintptr(w.scale(x)), uintptr(w.scale(y)),
		uintptr(w.scale(width)), uintptr(w.scale(height)), swpNoZOrder|swpNoActivate)
}

// centre sizes the window for its client area at the current DPI and
// centres it in the work area of the monitor it's on.
func (w *window) centre() {
	r := rect{Right: w.scale(clientW), Bottom: w.scale(clientH)}
	procAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)),
		wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox, 0, wsExCtrlParent, uintptr(w.dpi))
	width, height := r.Right-r.Left, r.Bottom-r.Top

	mon, _, _ := procMonitorFromWindow.Call(w.hwnd, monitorDefaultToNearest)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	x := mi.work.Left + (mi.work.Right-mi.work.Left-width)/2
	y := mi.work.Top + (mi.work.Bottom-mi.work.Top-height)/2
	procSetWindowPos.Call(w.hwnd, 0, uintptr(int64(x)), uintptr(int64(y)),
		uintptr(width), uintptr(height), swpNoZOrder|swpNoActivate)
}

func (w *window) freeResources() {
	for _, f := range []*uintptr{&w.font, &w.titleFont} {
		if *f != 0 {
			procDeleteObject.Call(*f)
			*f = 0
		}
	}
	for _, i := range []*uintptr{&w.bigIcon, &w.smallIcon} {
		tray.DestroyIconHandle(*i)
		*i = 0
	}
}

// loop pumps messages until the window is destroyed. IsDialogMessage
// gives it dialog keyboard handling: Tab between buttons, Enter for the
// default one (IDOK) and Escape for IDCANCEL.
func (w *window) loop() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		switch m.Message {
		case wmKeyDown, wmSysKeyDown, wmLButtonDown, wmRButtonDown, wmNCLButtonDown:
			w.stopCountdown() // someone is there: let them choose
		}
		if ok, _, _ := procIsDialogMessageW.Call(w.hwnd, uintptr(unsafe.Pointer(&m))); ok != 0 {
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (w *window) proc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCommand:
		w.command(wParam & 0xFFFF)
		return 0
	case wmTimer:
		w.timer(wParam)
		return 0
	case wmProgress:
		w.mu.Lock()
		text := w.progress
		w.mu.Unlock()
		w.setText(w.status, text)
		return 0
	case wmDone:
		w.done()
		return 0
	case wmCtlColorStatic, wmCtlColorBtn:
		// Draw text and button corners on the window colour instead of
		// the grey dialog default.
		c, _, _ := procGetSysColor.Call(colorWindow)
		procSetBkColor.Call(wParam, c)
		brush, _, _ := procGetSysColorBrush.Call(colorWindow)
		return brush
	case wmDpiChanged:
		w.applyDPI(int32(wParam & 0xFFFF))
		r := (*rect)(unsafe.Pointer(lParam))
		procSetWindowPos.Call(hwnd, 0, uintptr(int64(r.Left)), uintptr(int64(r.Top)),
			uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
		return 0
	case wmClose:
		if !w.running {
			win32.DestroyWindow(hwnd)
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	return win32.DefWindowProc(hwnd, message, wParam, lParam)
}

func (w *window) command(id uintptr) {
	switch {
	case w.running:
		return // nothing to do until the action finishes
	case w.finished && (id == idOK || id == idCancel || id == idClose):
		win32.DestroyWindow(w.hwnd)
	case w.finished:
	case id == idOK:
		w.start(true, false)
	case id == idUninstall:
		w.start(false, false)
	case id == idCancel:
		win32.DestroyWindow(w.hwnd) // Escape before choosing: do nothing
	}
}

func (w *window) timer(id uintptr) {
	switch id {
	case timerCountdown:
		expired := w.countdown.Tick()
		w.setText(w.install, w.countdown.Label("Install / update"))
		if expired {
			w.start(true, true)
		}
	case timerAutoClose:
		procKillTimer.Call(w.hwnd, timerAutoClose)
		win32.DestroyWindow(w.hwnd)
	}
}

func (w *window) stopCountdown() {
	if !w.countdown.Running() {
		return
	}
	w.countdown.Stop()
	procKillTimer.Call(w.hwnd, timerCountdown)
	w.setText(w.install, w.countdown.Label("Install / update"))
}

// start runs Install or Uninstall on a worker goroutine, which reports
// back through posted messages so the window never freezes.
func (w *window) start(install, auto bool) {
	w.stopCountdown()
	w.running, w.auto = true, auto
	procEnableWindow.Call(w.install, 0)
	procEnableWindow.Call(w.uninstall, 0)

	opts := w.opts
	opts.Progress = func(text string) {
		w.mu.Lock()
		w.progress = text
		w.mu.Unlock()
		win32.PostMessage(w.hwnd, wmProgress, 0, 0)
	}
	go func() {
		var tag string
		var err error
		if install {
			tag, err = Install(opts)
		} else {
			err = Uninstall(opts)
		}
		w.mu.Lock()
		w.tag, w.err = tag, err
		w.mu.Unlock()
		win32.PostMessage(w.hwnd, wmDone, boolArg(install), 0)
	}()
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// done shows the outcome and swaps the choice buttons for Close.
func (w *window) done() {
	w.mu.Lock()
	tag, err := w.tag, w.err
	w.mu.Unlock()
	w.running, w.finished = false, true

	var text string
	switch {
	case err != nil:
		text = "Failed: " + err.Error()
	case tag != "":
		text = fmt.Sprintf("Installed %s.", tag)
		if !w.opts.NoLaunch {
			text = fmt.Sprintf("Installed %s and started it - look for its icon in the system tray.", tag)
		}
	default:
		text = "Uninstalled."
	}
	delay := setupmenu.CloseDelay(w.auto, err)
	if delay > 0 {
		text += fmt.Sprintf(" Closing in %d seconds.", int(delay/time.Second))
		procSetTimer.Call(w.hwnd, timerAutoClose, uintptr(delay/time.Millisecond), 0)
	}
	w.setText(w.status, text)

	procShowWindow.Call(w.install, swHide)
	procShowWindow.Call(w.uninstall, swHide)
	procShowWindow.Call(w.closeBtn, swShow)
	procSendMessageW.Call(w.closeBtn, bmSetStyle, bsDefPushButton, 1)
	procSetFocus.Call(w.closeBtn)
}

func (w *window) setText(h uintptr, text string) {
	p, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procSetWindowTextW.Call(h, uintptr(unsafe.Pointer(p)))
}
