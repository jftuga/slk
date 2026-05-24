package overlay

import (
	"strings"
	"testing"
)

// TestDimmedOverlayPreservesWideCharacter guards the fix for the
// reaction-picker-shows-no-color-emoji bug. Composing a modal that
// contains a wide character (emoji 🔥, also CJK glyphs etc.) used to
// strip the glyph because the step-4 cell-copy loop called SetCell on
// the wide character's continuation column with an empty-content cell,
// overwriting the second column of the wide glyph.
func TestDimmedOverlayPreservesWideCharacter(t *testing.T) {
	background := strings.Repeat(strings.Repeat(" ", 40)+"\n", 10)
	box := "🔥 fire"
	out := DimmedOverlay(40, 10, background, box, 0.5)
	if !strings.Contains(out, "\U0001F525") {
		t.Errorf("DimmedOverlay output does NOT contain 🔥; cell-by-cell copy is dropping wide-character glyphs")
		t.Logf("output (first 200 bytes): %q", truncate(out, 200))
	}
}

// TestDimmedOverlayPreservesWideCharsInBackground guards that the
// SAME bug doesn't bite the step-1 dim-background pass. Even if the
// modal doesn't overlap a wide char in the background, the dim loop
// used to call SetCell on every cell — which (lipgloss bug) destroys
// wide-char glyphs even when called with the same cell back at the
// same position. Fix: mutate cell colors in-place via the live
// pointer CellAt returns; don't call SetCell at all.
func TestDimmedOverlayPreservesWideCharsInBackground(t *testing.T) {
	background := "🔥 burning\n" + strings.Repeat(strings.Repeat(" ", 40)+"\n", 9)
	box := "x" // tiny modal positioned in the middle; the emoji row stays uncovered
	out := DimmedOverlay(40, 10, background, box, 0.5)
	if !strings.Contains(out, "\U0001F525") {
		t.Errorf("DimmedOverlay background-dim pass dropped the 🔥 glyph from the un-modaled row")
		t.Logf("output (first 200 bytes): %q", truncate(out, 200))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
