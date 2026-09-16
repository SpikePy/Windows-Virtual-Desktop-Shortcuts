//go:build windows

package tray

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/desktopicon"
)

var (
	modGdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procCreateDIBSection   = modGdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = modGdi32.NewProc("CreateBitmap")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procCreateIconIndirect = modUser32.NewProc("CreateIconIndirect")
	procDestroyIcon        = modUser32.NewProc("DestroyIcon")
)

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// trayIconSize matches desktopicon.GridSize, so the tray glyph maps 1:1
// with no scaling needed.
const trayIconSize = desktopicon.GridSize

// pixel is BGRA order (what a 32bpp Windows DIB section expects).
type pixel struct{ B, G, R, A byte }

var (
	tilePixel   = pixel{B: 0x58, G: 0x4C, R: 0x42, A: 255}
	accentPixel = pixel{B: 0xED, G: 0x6F, R: 0x2F, A: 255}
	greyPixel   = pixel{B: 140, G: 140, R: 140, A: 255}
	strikePixel = pixel{B: 30, G: 30, R: 200, A: 255}
)

// buildDesktopIcon renders the four-tile desktop glyph as an alpha-blended
// HICON. For enabled=true the tiles are dark with the first one accented
// blue. For enabled=false the same glyph is drawn grey with a diagonal red
// strike across it - the conventional "disabled" cue - so it stays clearly
// recognizable against both light and dark taskbars. size is the icon's
// width and height in pixels.
func buildDesktopIcon(enabled bool, size int) (uintptr, error) {
	var bi bitmapInfoHeader
	bi.biSize = uint32(unsafe.Sizeof(bi))
	bi.biWidth = int32(size)
	bi.biHeight = -int32(size) // negative = top-down DIB, simpler indexing
	bi.biPlanes = 1
	bi.biBitCount = 32
	bi.biCompression = 0 // BI_RGB

	var bitsPtr uintptr
	hColor, _, e := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bitsPtr)), 0, 0)
	if hColor == 0 {
		return 0, fmt.Errorf("CreateDIBSection: %w", e)
	}
	pixels := unsafe.Slice((*pixel)(unsafe.Pointer(bitsPtr)), size*size)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			switch desktopicon.AtScaled(x, y, size) {
			case desktopicon.PartAccent:
				pixels[y*size+x] = accentPixel
			case desktopicon.PartTile:
				pixels[y*size+x] = tilePixel
			}
		}
	}

	if !enabled {
		for i := range pixels {
			if pixels[i].A != 0 {
				pixels[i] = greyPixel
			}
		}
		strike := 2 * size / trayIconSize
		// Diagonal strike, top-left to bottom-right, spanning the whole
		// canvas (including the transparent background) so it's
		// unambiguous at tray size regardless of glyph shape.
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if d := x - y; d >= -strike && d <= strike {
					pixels[y*size+x] = strikePixel
				}
			}
		}
	}

	// AND mask: all zero bits means "always use the color bitmap's own
	// alpha", the standard approach for a modern alpha-blended icon.
	// Monochrome bitmap rows are padded to a 16-bit boundary.
	maskBytes := make([]byte, (size+15)/16*2*size)
	hMask, _, e := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&maskBytes[0])))
	if hMask == 0 {
		procDeleteObject.Call(hColor)
		return 0, fmt.Errorf("CreateBitmap: %w", e)
	}

	ii := iconInfo{fIcon: 1, hbmMask: hMask, hbmColor: hColor}
	hIcon, _, e := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))

	// CreateIconIndirect copies the bitmap data internally; the source
	// bitmaps are ours to delete right away regardless of its outcome.
	procDeleteObject.Call(hColor)
	procDeleteObject.Call(hMask)

	if hIcon == 0 {
		return 0, fmt.Errorf("CreateIconIndirect: %w", e)
	}
	return hIcon, nil
}

// EnabledIcon returns the desktop glyph with the shortcuts active.
func EnabledIcon() (uintptr, error) { return buildDesktopIcon(true, trayIconSize) }

// DisabledIcon returns the same glyph greyed out with a diagonal red
// strike across it (shortcuts turned off).
func DisabledIcon() (uintptr, error) { return buildDesktopIcon(false, trayIconSize) }

// AppIcon returns the enabled glyph at size x size pixels, for showing the
// app's icon elsewhere, such as in the Setup window.
func AppIcon(size int) (uintptr, error) { return buildDesktopIcon(true, size) }

// DestroyIconHandle frees an HICON returned by EnabledIcon, DisabledIcon
// or AppIcon.
// Safe to call on a zero handle.
func DestroyIconHandle(hIcon uintptr) {
	if hIcon != 0 {
		procDestroyIcon.Call(hIcon)
	}
}
