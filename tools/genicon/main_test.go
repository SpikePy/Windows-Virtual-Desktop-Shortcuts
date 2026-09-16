package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/internal/desktopicon"
)

func TestRenderUsesTheGlyphColours(t *testing.T) {
	const size = 32
	img := render(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			got := [4]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
			var want [4]uint8
			switch desktopicon.AtScaled(x, y, size) {
			case desktopicon.PartAccent:
				want = [4]uint8{accentColor.R, accentColor.G, accentColor.B, 255}
			case desktopicon.PartTile:
				want = [4]uint8{tileColor.R, tileColor.G, tileColor.B, 255}
			default:
				want = [4]uint8{0, 0, 0, 0} // transparent background
			}
			if got != want {
				t.Fatalf("pixel (%d, %d) = %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestEncodeICO(t *testing.T) {
	want := []int{16, 32, 256}
	var imgs []*image.RGBA
	for _, s := range want {
		imgs = append(imgs, render(s))
	}

	data, err := encodeICO(imgs)
	if err != nil {
		t.Fatal(err)
	}

	var dir iconDir
	r := bytes.NewReader(data)
	if err := binary.Read(r, binary.LittleEndian, &dir); err != nil {
		t.Fatal(err)
	}
	if dir.Reserved != 0 || dir.Type != 1 || int(dir.Count) != len(want) {
		t.Fatalf("header = %+v, want reserved 0, type 1, count %d", dir, len(want))
	}

	for i, size := range want {
		var entry iconDirEntry
		if err := binary.Read(r, binary.LittleEndian, &entry); err != nil {
			t.Fatal(err)
		}
		// 256 is stored as 0, since the field is a single byte.
		wantDim := byte(size)
		if size >= 256 {
			wantDim = 0
		}
		if entry.Width != wantDim || entry.Height != wantDim {
			t.Errorf("frame %d: %dx%d, want %dx%d", i, entry.Width, entry.Height, wantDim, wantDim)
		}
		if entry.BitCount != 32 || entry.Planes != 1 {
			t.Errorf("frame %d: planes %d, bits %d; want 1, 32", i, entry.Planes, entry.BitCount)
		}
		if int(entry.ImageOffset)+int(entry.BytesInRes) > len(data) {
			t.Fatalf("frame %d: offset %d + size %d exceeds the %d-byte file", i, entry.ImageOffset, entry.BytesInRes, len(data))
		}

		frame := data[entry.ImageOffset : entry.ImageOffset+entry.BytesInRes]
		img, err := png.Decode(bytes.NewReader(frame))
		if err != nil {
			t.Fatalf("frame %d is not valid PNG: %v", i, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("frame %d decodes to %dx%d, want %dx%d", i, b.Dx(), b.Dy(), size, size)
		}
	}
}

// The .syso files are generated once and committed, so nothing rebuilds
// them when the glyph or the manifest changes. This catches a stale one:
// every PNG frame genicon renders must appear in each exe's resources, and
// Setup's must carry its manifest verbatim. See DETAILS.md for the
// commands that regenerate them.
func TestCommittedResourcesAreCurrent(t *testing.T) {
	var frames [][]byte
	for _, size := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(size)); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, buf.Bytes())
	}

	for _, dir := range []string{"virtualdesktopshortcuts", "vds-setup"} {
		syso, err := os.ReadFile("../../cmd/" + dir + "/rsrc_windows_amd64.syso")
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range frames {
			if !bytes.Contains(syso, f) {
				t.Errorf("cmd/%s: the %dpx icon frame is out of date - regenerate the .syso", dir, sizes[i])
			}
		}
	}

	manifest, err := os.ReadFile("../../cmd/vds-setup/setup.manifest")
	if err != nil {
		t.Fatal(err)
	}
	syso, err := os.ReadFile("../../cmd/vds-setup/rsrc_windows_amd64.syso")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(syso, manifest) {
		t.Error("cmd/vds-setup: setup.manifest is not what the .syso embeds - regenerate the .syso")
	}
}
