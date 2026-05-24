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

// TestPickerRendersHeartGlyph guards that VS16-anchored emoji
// (single base + U+FE0F) DO render as a glyph. This is the case
// that the picker's previous len(runes)==1 rule incorrectly
// excluded — surfacing it again was a key win of the
// ShouldRenderUnicode-based rule.
func TestPickerRendersHeartGlyph(t *testing.T) {
	m := New()
	m.SetFrecentEmoji([]EmojiEntry{})
	m.Open("Cxxx", "1.0", nil)
	for _, ch := range "heart" {
		m.HandleKey(string(ch))
	}
	view := m.View(120)
	if !strings.Contains(view, "\u2764\uFE0F") {
		t.Errorf("rendered picker does NOT contain ❤️ glyph (VS16-anchored) for :heart: search")
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
