package desktopicon

import "testing"

// tileCentre returns the middle of the tile at (col, row) on a size canvas,
// mirroring the geometry AtScaled derives.
func tileCentre(col, row, size int) (int, int) {
	margin := size * 3 / GridSize
	gap := size * 4 / GridSize
	tile := (size - 2*margin - gap) / 2
	at := func(index int) int {
		start := margin
		if index == 1 {
			start = margin + tile + gap
		}
		return start + tile/2
	}
	return at(col), at(row)
}

func TestAtTileCentres(t *testing.T) {
	for _, size := range []int{16, 24, 32, 48, 256} {
		for row := 0; row < 2; row++ {
			for col := 0; col < 2; col++ {
				x, y := tileCentre(col, row, size)
				got := AtScaled(x, y, size)
				want := PartTile
				if col == 0 && row == 0 {
					want = PartAccent
				}
				if got != want {
					t.Errorf("size %d: AtScaled(%d, %d) = %v, want %v", size, x, y, got, want)
				}
			}
		}
	}
}

func TestAtBackground(t *testing.T) {
	const size = GridSize
	tests := []struct {
		name string
		x, y int
	}{
		{"top-left corner", 0, 0},
		{"bottom-right corner", size - 1, size - 1},
		{"centre gap", size / 2, size / 2},
		{"left margin", 0, size / 2},
		{"outside the canvas", size, size},
		{"negative", -1, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AtScaled(tt.x, tt.y, size); got != PartNone {
				t.Errorf("AtScaled(%d, %d) = %v, want PartNone", tt.x, tt.y, got)
			}
		})
	}
}

func TestAtMatchesAtScaledAtGridSize(t *testing.T) {
	for y := 0; y < GridSize; y++ {
		for x := 0; x < GridSize; x++ {
			if got, want := At(x, y), AtScaled(x, y, GridSize); got != want {
				t.Fatalf("At(%d, %d) = %v, AtScaled = %v", x, y, got, want)
			}
		}
	}
}

// The glyph has to stay recognisable at tray size, which means each tile
// keeps real area and the four of them stay separated.
func TestGlyphCoverage(t *testing.T) {
	for _, size := range []int{16, 32, 48} {
		var covered int
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if AtScaled(x, y, size) != PartNone {
					covered++
				}
			}
		}
		total := size * size
		if covered*100/total < 20 || covered*100/total > 60 {
			t.Errorf("size %d: glyph covers %d%% of the canvas, want 20-60%%", size, covered*100/total)
		}
	}
}
