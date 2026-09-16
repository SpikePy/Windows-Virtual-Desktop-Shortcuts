// Package desktopicon describes the glyph used for both the tray icon and
// the .exe file icon: four rounded-off tiles in a 2x2 grid, standing for
// virtual desktops, with the first one accented. It is pure geometry with
// no OS dependency, so the tray (which paints it into a Windows bitmap)
// and tools/genicon (which renders it into an .ico) can share one
// definition, and it can be tested anywhere.
package desktopicon

// GridSize is the canvas the glyph is defined on. Other sizes are derived
// by scaling in AtScaled.
const GridSize = 32

// Part identifies what the glyph covers at a given pixel.
type Part int

const (
	// PartNone is the transparent background between and around tiles.
	PartNone Part = iota
	// PartTile is one of the three plain tiles.
	PartTile
	// PartAccent is the top-left tile, drawn in the accent colour to mark
	// "the desktop you're on".
	PartAccent
)

// At returns the part at (x, y) on a GridSize x GridSize canvas.
func At(x, y int) Part { return AtScaled(x, y, GridSize) }

// AtScaled returns the part at (x, y) on a size x size canvas. Every edge
// is an axis-aligned rectangle derived from size, so the glyph stays crisp
// at any resolution instead of being resampled.
func AtScaled(x, y, size int) Part {
	if size <= 0 || x < 0 || y < 0 || x >= size || y >= size {
		return PartNone
	}

	// Margin around the grid and gap between the tiles, as fractions of
	// the canvas; the tiles take whatever is left.
	margin := size * 3 / GridSize
	gap := size * 4 / GridSize
	tile := (size - 2*margin - gap) / 2
	if tile <= 0 {
		return PartNone
	}

	col, colOK := cell(x, margin, gap, tile)
	row, rowOK := cell(y, margin, gap, tile)
	if !colOK || !rowOK {
		return PartNone
	}
	// Clip the outermost corner pixel of each tile, which reads as a
	// rounded corner at tray size without needing real anti-aliasing.
	if round := tile / 8; round > 0 && corner(x, col, margin, gap, tile, round) && corner(y, row, margin, gap, tile, round) {
		return PartNone
	}
	if col == 0 && row == 0 {
		return PartAccent
	}
	return PartTile
}

// cell maps a coordinate to tile 0 or 1, reporting false when it falls in
// the margin or the gap between them.
func cell(v, margin, gap, tile int) (int, bool) {
	switch {
	case v < margin:
		return 0, false
	case v < margin+tile:
		return 0, true
	case v < margin+tile+gap:
		return 0, false
	case v < margin+tile+gap+tile:
		return 1, true
	default:
		return 0, false
	}
}

// corner reports whether v lies within round pixels of the outer edge of
// the tile it belongs to.
func corner(v, index, margin, gap, tile, round int) bool {
	start := margin
	if index == 1 {
		start = margin + tile + gap
	}
	return v < start+round || v >= start+tile-round
}
