package emoji

import "strings"

// vs16 is U+FE0F, the variation selector that forces emoji presentation
// for the preceding base codepoint. VS16 is well-supported across
// terminal fonts because no glyph composition is required — the
// terminal merely picks the emoji-presentation form of a single base
// codepoint that the font already has.
const vs16 rune = 0xFE0F

// ShouldRenderUnicode reports whether the resolved Unicode form of an
// emoji is composition-safe to render as a glyph. Returns true iff the
// string contains exactly one base codepoint, or exactly one base
// codepoint followed by VS16. Multi-codepoint sequences (ZWJ,
// regional-indicator flag pairs, skin-tone modifiers) return false
// because terminal font support for composition is inconsistent and
// the resulting visual-width disagreement breaks slk's layout.
//
// Trailing whitespace is ignored — kyokomi/emoji's Sprint appends a
// trailing space after each resolved emoji and callers occasionally
// pass that form. Empty strings (after trim) return false.
func ShouldRenderUnicode(s string) bool {
	s = strings.TrimRight(s, " \t")
	if s == "" {
		return false
	}
	runes := []rune(s)
	switch len(runes) {
	case 1:
		return true
	case 2:
		return runes[1] == vs16
	default:
		return false
	}
}
