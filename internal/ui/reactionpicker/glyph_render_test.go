package reactionpicker

import (
	"strings"
	"testing"
)

// TestPickerRendersFireGlyph guards the composition-safe emoji
// fallback fix at /home/grant/local_code/slk/docs/superpowers/specs/2026-05-24-emoji-shortcode-fallback-design.md.
// Opens the picker, types "fire", renders the frame, and asserts the
// 🔥 glyph (U+1F525) is present in the output — confirming the
// picker did NOT fall back to literal ":fire:" text for a simple
// single-codepoint emoji.
//
// Regression target: the picker's previous len(runes)==1 rule also
// reported true for :fire: so this test would have passed before too.
// What it really guards is the broader ShouldRenderUnicode wiring:
// if a future refactor accidentally hides single-codepoint emoji
// behind the shortcode fallback, this test fails.
func TestPickerRendersFireGlyph(t *testing.T) {
	m := New()
	m.SetFrecentEmoji([]EmojiEntry{})
	m.Open("Cxxx", "1.0", nil)
	for _, ch := range "fire" {
		m.HandleKey(string(ch))
	}
	view := m.View(120)
	if !strings.Contains(view, "\U0001F525") {
		t.Errorf("rendered picker does NOT contain 🔥 glyph for :fire: search")
		lines := strings.Split(view, "\n")
		for i, ln := range lines {
			if i > 20 {
				break
			}
			t.Logf("line %d: %q", i, ln)
		}
	}
}

// TestViewOverlayPreservesFireGlyph guards that going through
// ViewOverlay (which uses lipgloss.NewCanvas cell-by-cell compositing)
// preserves the wide emoji glyph. Caught a real bug where the canvas
// overlay path was dropping wide-character cells.
func TestViewOverlayPreservesFireGlyph(t *testing.T) {
	m := New()
	m.SetFrecentEmoji([]EmojiEntry{})
	m.Open("Cxxx", "1.0", nil)
	for _, ch := range "fire" {
		m.HandleKey(string(ch))
	}
	background := strings.Repeat(" \n", 30)
	overlaid := m.ViewOverlay(120, 30, background)
	if !strings.Contains(overlaid, "\U0001F525") {
		t.Errorf("ViewOverlay output does NOT contain 🔥 glyph (canvas/overlay compositing stripped it)")
		// Compare against View() output to confirm it's the overlay path
		view := m.View(120)
		hasFireInView := strings.Contains(view, "\U0001F525")
		t.Logf("View() contains 🔥: %v (if true, bug is in ViewOverlay/DimmedOverlay)", hasFireInView)
	}
}

// TestPickerFallsBackForVS16Emoji guards that VS16-anchored emoji
// (single base + U+FE0F) now fall back to :name: text rather than
// rendering the Unicode glyph. Many terminal+font combos render
// VS16 emoji from legacy blocks at a different visual width than
// lipgloss reports, breaking border alignment; the shortcode form
// is layout-safe.
func TestPickerFallsBackForVS16Emoji(t *testing.T) {
	m := New()
	m.SetFrecentEmoji([]EmojiEntry{})
	m.Open("Cxxx", "1.0", nil)
	for _, ch := range "heart" {
		m.HandleKey(string(ch))
	}
	view := m.View(120)
	if !strings.Contains(view, ":heart:") {
		t.Errorf("rendered picker does NOT contain literal :heart: text for VS16-anchored emoji")
	}
	if strings.Contains(view, "\u2764\uFE0F") {
		t.Errorf("rendered picker contains ❤️ Unicode glyph; expected :heart: text fallback")
	}
}

// TestPickerFallsBackForZWJSequence guards the OTHER side of the
// fix: composition-fragile emoji (ZWJ sequences, here the pride
// flag) must fall back to the literal :name: text, NOT render as
// the broken-glyph Unicode sequence.
func TestPickerFallsBackForZWJSequence(t *testing.T) {
	m := New()
	m.SetFrecentEmoji([]EmojiEntry{})
	m.Open("Cxxx", "1.0", nil)
	for _, ch := range "rainbow_f" {
		m.HandleKey(string(ch))
	}
	view := m.View(120)
	if !strings.Contains(view, ":rainbow_flag:") {
		t.Errorf("rendered picker does NOT contain literal :rainbow_flag: for ZWJ-sequence emoji")
	}
	// And it must NOT contain the actual ZWJ pride flag glyph.
	if strings.Contains(view, "\U0001F3F3\uFE0F\u200D\U0001F308") {
		t.Errorf("rendered picker contains 🏳️‍🌈 Unicode glyph; expected :rainbow_flag: text fallback")
	}
}
