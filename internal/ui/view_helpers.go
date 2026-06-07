// internal/ui/view_helpers.go
//
// Phase 6 of the SOLID refactor of internal/ui/app.go: per-region
// View renderer extraction.
//
// This file holds shared primitives used by the per-region
// renderers in view_*.go (and still by App.View itself until all
// regions are extracted).
//
// Both helpers were originally inline closures at the top of
// App.View. Hoisting them out is a prerequisite for the region
// split -- per-region renderers in view_*.go need to call them
// without capturing the View-scoped closure environment.
//
// Both are pure (no App state, no goroutines, no allocations
// beyond what lipgloss does internally). The Go compiler inlines
// the no-capture closures into their call site; the free-function
// form is bytecode-equivalent.
package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/gammons/slk/internal/ui/styles"
)

// joinPanelsHorizontal is a zero-measurement replacement for
// lipgloss.JoinHorizontal(lipgloss.Top, panels...) for the App.View
// composite. The view's panels (rail, sidebar, messages, thread) are each
// built to exactly `height` rows of a uniform per-panel width (via
// exactSize / borderedTopPane), so lipgloss's per-line width measurement
// and height padding are pure overhead -- the join is just a row-wise
// concatenation.
//
// It returns ok=false (and the caller falls back to lipgloss) if ANY
// panel does not have exactly `height` rows, which is the only condition
// under which lipgloss's height-padding would actually differ from a
// naive concat (e.g. a clamped pane on a very short terminal, or the
// preview panel). Uniform per-panel line width is guaranteed by the
// renderers and asserted by TestJoinPanelsHorizontal_MatchesLipgloss.
func joinPanelsHorizontal(panels []string, height int) (string, bool) {
	if len(panels) == 0 || height <= 0 {
		return "", false
	}
	cols := make([][]string, len(panels))
	for i, p := range panels {
		lines := strings.Split(p, "\n")
		if len(lines) != height {
			return "", false
		}
		cols[i] = lines
	}
	var b strings.Builder
	for r := 0; r < height; r++ {
		if r > 0 {
			b.WriteByte('\n')
		}
		for i := range cols {
			b.WriteString(cols[i][r])
		}
	}
	return b.String(), true
}

// stackContentStatus is a near-zero-measurement replacement for
// lipgloss.JoinVertical(lipgloss.Left, content, status) for the two
// final blocks of App.View. content is the horizontally-joined panel
// block (uniform line width); status is the single status row. lipgloss
// left-aligns by padding every line of the narrower block to the wider
// block's width with plain spaces -- so we measure just one content line
// (content is uniform) and the status row, then pad with constant runs.
//
// This reproduces lipgloss's output byte-for-byte (verified by
// TestViewComposite_MatchesLipgloss), INCLUDING the case where the status
// row is wider than the content (a pre-existing statusbar width quirk):
// lipgloss right-pads the content lines to the status width, and so do we.
func stackContentStatus(content, status string) string {
	contentW := 0
	if nl := strings.IndexByte(content, '\n'); nl >= 0 {
		contentW = ansi.StringWidth(content[:nl])
	} else {
		contentW = ansi.StringWidth(content)
	}
	statusW := ansi.StringWidth(status)
	maxW := contentW
	if statusW > maxW {
		maxW = statusW
	}

	var b strings.Builder
	if maxW > contentW {
		pad := strings.Repeat(" ", maxW-contentW)
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(line)
			b.WriteString(pad)
		}
	} else {
		b.WriteString(content)
	}
	b.WriteByte('\n')
	b.WriteString(status)
	if maxW > statusW {
		b.WriteString(strings.Repeat(" ", maxW-statusW))
	}
	return b.String()
}

// exactSizeBg forces s to exactly (w, h) cells with bg as the
// background color. Uses an explicit width parameter instead of
// lipgloss.Width(s) to avoid ANSI miscounting in complex rendered
// content (e.g. nested borders, mixed-foreground spans).
func exactSizeBg(s string, w, h int, bg color.Color) string {
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Background(bg).Render(s)
}

// exactSize is exactSizeBg with the default theme background.
// The vast majority of pane renders want this; only the workspace
// rail and sidebar (which use distinct panel colors) reach for
// exactSizeBg directly.
func exactSize(s string, w, h int) string {
	return exactSizeBg(s, w, h, styles.Background)
}

// padPaneToSize is a fast replacement for exactSize when the caller can
// guarantee every line of s is already exactly srcWidth display cells
// wide (e.g. the output of a lipgloss border style with a fixed .Width(),
// which pads every line to that width). It appends (w - srcWidth)
// background gutter columns to each line and enforces exactly h rows --
// skipping exactSize's per-line, grapheme-by-grapheme width
// re-measurement, which is the single largest per-frame allocation/CPU
// cost on the message-pane scroll hot path at large terminal sizes
// (lipgloss WrapWriter ~70% of scroll-frame allocations).
//
// Output is VISUALLY identical to exactSize(s, w, h): same row count,
// same per-line display width, same colors, same plain text. Only
// redundant SGR bytes (a leading background escape + an extra reset that
// exactSize emits per line) differ; they render identically and the
// downstream selection overlay / bubbletea diff handle arbitrary SGR.
//
// srcWidth MUST be the true uniform width of s's lines; passing a wrong
// value yields a misaligned right edge. Use exactSize when line widths
// are not statically known.
// borderedTopPane assembles a top-bordered pane (top edge + left/right
// verticals, NO bottom edge) around content whose every line is already
// EXACTLY innerWidth display cells, then right-pads each row to fullWidth
// and clamps to exactly rows rows. It is the zero-measurement equivalent
// of the messages-pane hot path:
//
//	padPaneToSize(
//	    borderStyle.Width(innerWidth+2).BorderTop(true).BorderLeft(true).
//	        BorderRight(true).BorderBottom(false).Render(content),
//	    innerWidth+2, fullWidth, rows, bg)
//
// The lipgloss form re-measures every content line grapheme-by-grapheme
// every frame (the dominant cost when scrolling a tall pane on a wide
// terminal). Because the cached message lines are built to a fixed width,
// this form instead concatenates a constant left/right border cell around
// each line -- the only per-line work is byte concatenation. The border
// glyphs and colors are taken from the SAME lipgloss style the old path
// used (via the Get*Border accessors) so the result is visually identical.
//
// INVARIANT: every line of content MUST be exactly innerWidth display
// cells. Callers guarantee this by padding every source line at build
// time (see messages.Model: chrome, spacer, "more below", loading hint,
// and per-entry lines are all width-padded). The equivalence test
// TestBorderedTopPane_MatchesLipgloss asserts this across states; if a
// new unpadded source is introduced, that test fails (a short line yields
// a row narrower than fullWidth).
func borderedTopPane(content string, innerWidth, fullWidth, rows int, focused bool, bg color.Color) string {
	bs := styles.UnfocusedBorder
	if focused {
		bs = styles.FocusedBorder
	}
	bd := bs.GetBorderStyle()
	fg := bs.GetBorderTopForeground()
	bbg := bs.GetBorderTopBackground()

	edge := lipgloss.NewStyle().Foreground(fg).Background(bbg)
	left := edge.Render(bd.Left)
	right := edge.Render(bd.Right)
	topEdge := edge.Render(bd.TopLeft + strings.Repeat(bd.Top, innerWidth) + bd.TopRight)

	gutterCols := fullWidth - (innerWidth + 2)
	gutter := ""
	if gutterCols > 0 {
		gutter = lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", gutterCols))
	}
	blankInner := lipgloss.NewStyle().Background(bg).Width(innerWidth).Render("")

	lines := strings.Split(content, "\n")
	var b strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteByte('\n')
		}
		if r == 0 {
			b.WriteString(topEdge)
			b.WriteString(gutter)
			continue
		}
		ci := r - 1
		b.WriteString(left)
		if ci < len(lines) {
			b.WriteString(lines[ci])
		} else {
			b.WriteString(blankInner)
		}
		b.WriteString(right)
		b.WriteString(gutter)
	}
	return b.String()
}

func padPaneToSize(s string, srcWidth, w, h int, bg color.Color) string {
	gutterCols := w - srcWidth
	gutter := ""
	if gutterCols > 0 {
		gutter = lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", gutterCols))
	}
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i < len(lines) {
			b.WriteString(lines[i])
			if gutterCols > 0 {
				b.WriteString(gutter)
			}
		} else {
			// Height underflow: themed blank row at the full width
			// (matches exactSize's vertical padding behavior).
			b.WriteString(lipgloss.NewStyle().Background(bg).Width(w).Render(""))
		}
	}
	return b.String()
}
