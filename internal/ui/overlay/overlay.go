package overlay

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/gammons/slk/internal/image"
)

// kittyPlaceholderPrefix is the leading byte sequence of any cell whose
// content begins with image.PlaceholderRune (U+10EEEE encoded as UTF-8:
// 0xF4 0x8E 0xBB 0xAE). We match against the Cell.Content string rather
// than the rune to handle the diacritic-suffixed graphemes kitty's
// renderer emits without taking a dependency on cluster segmentation.
var kittyPlaceholderPrefix = string(image.PlaceholderRune)

// DimmedOverlay composites a modal box on top of a dimmed background.
// The background string is rendered to a Canvas, all cell colors are
// darkened by dimPercent (0.0-1.0), then the modal box is placed centered
// on top by copying its cells.
//
// Cells whose content carries the kitty unicode-placeholder rune are
// blanked: their FG is a 24-bit encoding of an image ID (not a visual
// color), so darkening it would mutate the ID and surface a different
// image — see internal/image/kitty.go. Replacing the placeholder rune
// with a space drops the kitty placement for the duration of the
// overlay; the image data stays uploaded and the next non-overlay
// frame re-emits the placeholder cells, re-creating the placement
// without any image-state plumbing. Issue #18.
func DimmedOverlay(width, height int, background string, box string, dimPercent float64) string {
	// Step 1: Render background to canvas and dim all cells
	canvas := lipgloss.NewCanvas(width, height)
	canvas.Compose(lipgloss.NewLayer(background))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cell := canvas.CellAt(x, y)
			if cell == nil {
				continue
			}
			if strings.HasPrefix(cell.Content, kittyPlaceholderPrefix) {
				// Drop the kitty placement at this cell. Clear the FG
				// (was image-ID-as-RGB, not a real color) and let the
				// normal dim path tint any BG so the region blends
				// into the rest of the dimmed background.
				cell.Content = " "
				cell.Width = 1
				cell.Style.Fg = nil
			}
			if cell.Style.Bg != nil {
				cell.Style.Bg = lipgloss.Darken(cell.Style.Bg, dimPercent)
			}
			if cell.Style.Fg != nil {
				cell.Style.Fg = lipgloss.Darken(cell.Style.Fg, dimPercent)
			}
			canvas.SetCell(x, y, cell)
		}
	}

	// Step 2: Render dimmed canvas, create output canvas
	dimmedStr := canvas.Render()
	outCanvas := lipgloss.NewCanvas(width, height)
	outCanvas.Compose(lipgloss.NewLayer(dimmedStr))

	// Step 3: Render modal to its own canvas, compute centered position
	modalW := lipgloss.Width(box)
	modalH := lipgloss.Height(box)
	startX := (width - modalW) / 2
	startY := (height - modalH) / 2
	if startX < 0 {
		startX = 0
	}
	if startY < 0 {
		startY = 0
	}

	modalCanvas := lipgloss.NewCanvas(modalW, modalH)
	modalCanvas.Compose(lipgloss.NewLayer(box))

	// Step 4: Copy modal cells onto output canvas
	for my := 0; my < modalH; my++ {
		for mx := 0; mx < modalW; mx++ {
			cell := modalCanvas.CellAt(mx, my)
			if cell != nil {
				outCanvas.SetCell(startX+mx, startY+my, cell)
			}
		}
	}

	return outCanvas.Render()
}
