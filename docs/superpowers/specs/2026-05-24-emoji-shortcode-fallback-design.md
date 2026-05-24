# Emoji shortcode fallback for composition-fragile sequences

## Problem

Reactions and inline message-body emojis whose Unicode form is a
multi-codepoint sequence (ZWJ sequences, regional-indicator flag
pairs, skin-tone modifiers, VS16-anchored compositions) corrupt slk's
TUI layout when the user's terminal font cannot compose the sequence
into a single combined glyph.

Concretely observed in kitty + ghostty with their default font stack:
the pride flag emoji `🏳️‍🌈` (U+1F3F3 U+FE0F U+200D U+1F308) is
treated by lipgloss/displaywidth as a width-2 grapheme cluster, but
the terminal font draws the parts as two separate glyphs (white flag
+ rainbow). The cursor advance and the physical pixel rendering
disagree, and slk's right border lands one column off on the affected
row. Bug reproduces with `:rainbow-flag:` reactions in any DM/channel.

The root cause is fundamentally outside slk: it is a disagreement
between three layers (Unicode width tables, terminal width tables,
font glyph coverage). slk cannot fix the terminal or the font. What
slk CAN do is decline to render compositionally fragile emoji as
glyphs, and instead show their Slack-style `:shortcode:` text. Plain
ASCII text has no width ambiguity.

Precedent: `internal/ui/reactionpicker/model.go:340-351` already does
this, with an explanatory comment. The rule it uses
(`len([]rune(unicode)) == 1`) is over-restrictive — it excludes all
VS16-anchored emoji (`❤️`, `⚠️`, `🏳️`) which most terminal fonts
DO render correctly. The picker is consequently missing nearly all
its colorful emoji in the user's terminal.

## Solution

Add a single shared helper that classifies any resolved Unicode emoji
string as "safe to render as a glyph" or "must fall back to text
shortcode". The rule:

> Render as glyph iff the string is exactly one codepoint, OR exactly
> two codepoints where the second is VS16 (U+FE0F). Otherwise render
> the `:shortcode:` text.

VS16 is well-supported: it merely tells the terminal to use
emoji-presentation for a single base codepoint, no composition
required. The composition-fragile cases (ZWJ, regional indicators,
skin tones) all involve more than one base codepoint and are the ones
that misbehave when a font lacks the combined glyph.

Apply at every site that resolves shortcode → Unicode for display:

- `internal/ui/messages/model.go:1635` — message-pane reaction pill
- `internal/ui/thread/model.go:1595` — thread-pane reaction pill
- `internal/ui/reactionpicker/model.go:347` — picker list (replaces
  the over-restrictive `len(runes)==1` test, surfacing more colorful
  emoji)
- `internal/ui/messages/render.go:564` — message body inline emoji.
  The current single `emoji.Sprint(text)` call replaces every
  shortcode in one pass. To apply the per-emoji rule, replace it with
  a scan that resolves each `:name:` individually: keep the
  shortcode text when the resolved Unicode fails the rule, swap in
  the Unicode glyph when it passes.

## Helper API

`internal/emoji/shouldrender.go`:

```go
// ShouldRenderUnicode reports whether the rendered Unicode form of
// an emoji is composition-safe: exactly one codepoint, or one
// codepoint followed by VS16 (U+FE0F). Multi-codepoint sequences
// (ZWJ, regional-indicator flag pairs, skin-tone modifiers) are
// rejected because terminal font support for composition is
// inconsistent and visual width disagreement breaks slk's layout.
//
// Callers that resolve a :shortcode: via kyokomi/emoji should use
// this to decide whether to render the Unicode glyph or keep the
// shortcode as readable text.
func ShouldRenderUnicode(unicode string) bool
```

The implementation walks runes with a tiny state machine; no
allocations. Empty string returns false.

Truth-table tests in `internal/emoji/shouldrender_test.go` cover:

- single emoji (`🙌` → true)
- single non-emoji codepoint (`a` → true; harmless edge case)
- emoji + VS16 (`❤️` → true)
- non-emoji + VS16 (`#️` → true; rule is structural, not semantic)
- ZWJ sequence (`🏳️‍🌈` → false)
- regional indicator pair (`🇺🇸` → false)
- skin-tone modifier (`👍🏽` → false)
- text + VS16 + extra codepoint (false)
- empty string (false)

## Body-text rewrite

The current body-text path is a single `emoji.Sprint(text)` call that
substitutes every `:shortcode:` in one pass via kyokomi's internal
scanner. Per-emoji control requires us to resolve emojis one at a
time. Plan:

1. Add a helper in `internal/emoji/` that scans a string for
   `:shortcode:` runs, resolves each via the existing codemap +
   custom-emoji map, applies `ShouldRenderUnicode` to the resolved
   value, and emits either the Unicode glyph (with kyokomi's
   trailing-space behavior preserved for compatibility) or the
   literal shortcode.
2. `renderInlineFormatting` calls the new helper instead of
   `emoji.Sprint`.
3. Custom workspace emoji (`emoji.BuildEntries` / `SetCustomEmoji`)
   continue to resolve via the existing alias-chain machinery; the
   resolved Unicode is fed through `ShouldRenderUnicode` like any
   other.

## Cleanup of debugging artifacts

The investigation that led to this design left several temporary
additions that must be reverted before shipping:

- `internal/emoji/sprint.go` — the `SLK_NO_EMOJI` env-var wrapper.
  Delete the file; the new fix is the proper solution.
- `internal/emoji/test_helpers.go` — `SetWidthMapForTest`. Keep if
  the new tests use it; otherwise delete.
- `internal/ui/messages/model.go` — `debugCheckModal` helper.
  Remove.
- `internal/ui/view_messages.go` — `debugDumpLineWidths`,
  `dumpFrameIfRequested`, the per-stage instrumentation in
  `renderMessagesTop`. Remove. Restore the original imports.
- `internal/ui/messages/width_repro_test.go` — rewrite as an
  assertion that ZWJ-reaction messages no longer corrupt borders
  (using the new helper); or delete if redundant with the helper's
  truth-table tests.
- All call sites that switched from `emoji.Sprint` (kyokomi) to
  `emojiutil.Sprint` (the SLK_NO_EMOJI wrapper) get updated to call
  the new resolution helper. Restore unused-import cleanups.

## Out of scope

- A probe-based detection that asks the terminal "do you actually
  compose this ZWJ sequence into a single glyph?" — desirable but
  hard (DSR-style probing only reports cursor advance, which equals
  the displaywidth value regardless of glyph coverage). Filed as a
  follow-up after this ships.
- Detecting and fixing the underlying lipgloss/displaywidth ZWJ
  miscount — upstream issue.
- Replacing kyokomi's trailing-space behavior — orthogonal.

## Acceptance

- Reaction pill containing `:rainbow-flag:` no longer breaks the
  right border on any row.
- Reaction picker shows colorful emoji for `:heart:`, `:warning:`,
  `:rainbow:`, and other VS16-class emoji (previously hidden by the
  over-restrictive `len(runes)==1` rule).
- Reaction pills containing skin-toned, ZWJ-composed, or flag
  emoji render as readable shortcodes instead of broken glyphs.
- Message body containing inline `:rainbow-flag:` etc. no longer
  breaks borders.
- All existing tests pass. New truth-table tests for
  `ShouldRenderUnicode` pass.
- All temporary debug instrumentation removed.
