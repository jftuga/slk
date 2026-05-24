# Emoji Shortcode Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop slk from rendering composition-fragile emoji (ZWJ sequences, flag pairs, skin-tone modifiers) as Unicode glyphs. Fall back to readable `:shortcode:` text whenever the resolved Unicode form is more than one base codepoint + optional VS16. Fixes the right-border-breaks-on-emoji bug in kitty/ghostty without requiring users to set an env var.

**Architecture:** Single shared classifier (`emoji.ShouldRenderUnicode`) used at every shortcode→Unicode resolution site (message-pane reaction pills, thread-pane reaction pills, reaction picker, message body text). The rule: render Unicode iff the string is exactly one codepoint, OR one codepoint followed by VS16 (U+FE0F). Everything else is shown as its `:name:` form.

**Tech Stack:** Go 1.21+, `github.com/kyokomi/emoji/v2`, `charm.land/lipgloss/v2`. Reuses existing `internal/emoji/` package.

**Spec:** `docs/superpowers/specs/2026-05-24-emoji-shortcode-fallback-design.md`

---

## Task 1: Add ShouldRenderUnicode helper with truth-table tests

**Files:**
- Create: `internal/emoji/shouldrender.go`
- Create: `internal/emoji/shouldrender_test.go`

- [ ] **Step 1.1: Write the failing tests**

Create `internal/emoji/shouldrender_test.go`:

```go
package emoji

import "testing"

func TestShouldRenderUnicode(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    bool
	}{
		// Single codepoint, emoji presentation.
		{"single emoji raised hands", "\U0001F64C", true}, // 🙌
		{"single emoji rainbow", "\U0001F308", true},      // 🌈
		{"single emoji fire", "\U0001F525", true},          // 🔥
		// Single non-emoji codepoint (rule is structural; harmless).
		{"single ascii letter", "a", true},
		{"single misc symbol", "\u2603", true}, // ☃
		// Single base codepoint + VS16 (well-supported composition).
		{"heart + VS16", "\u2764\uFE0F", true},        // ❤️
		{"warning + VS16", "\u26A0\uFE0F", true},      // ⚠️
		{"flag base + VS16", "\U0001F3F3\uFE0F", true}, // 🏳️ (white flag alone)
		// Multi-codepoint composition (the cases the bug is about).
		{"ZWJ pride flag", "\U0001F3F3\uFE0F\u200D\U0001F308", false}, // 🏳️‍🌈
		{"ZWJ family", "\U0001F468\u200D\U0001F469\u200D\U0001F467", false}, // 👨‍👩‍👧
		{"regional indicator pair US", "\U0001F1FA\U0001F1F8", false}, // 🇺🇸
		{"skin-tone modifier", "\U0001F44D\U0001F3FD", false},          // 👍🏽
		{"base + VS16 + extra codepoint", "\u2764\uFE0F\u200D\U0001F525", false}, // ❤️‍🔥
		// Edge cases.
		{"empty string", "", false},
		{"two ascii letters", "ab", false},
		// Defensive: callers occasionally pass kyokomi's trailing-space
		// form. The helper trims trailing whitespace before classifying.
		{"single emoji with trailing space", "\U0001F308 ", true},
		{"ZWJ with trailing space", "\U0001F3F3\uFE0F\u200D\U0001F308 ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ShouldRenderUnicode(c.input)
			if got != c.want {
				t.Errorf("ShouldRenderUnicode(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 1.2: Run tests to verify they fail**

Run: `cd /home/grant/local_code/slk && go test ./internal/emoji -run TestShouldRenderUnicode -v`
Expected: FAIL with "undefined: ShouldRenderUnicode" (or equivalent build error).

- [ ] **Step 1.3: Implement the helper**

Create `internal/emoji/shouldrender.go`:

```go
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
```

- [ ] **Step 1.4: Run tests to verify they pass**

Run: `cd /home/grant/local_code/slk && go test ./internal/emoji -run TestShouldRenderUnicode -v`
Expected: All 17 sub-tests PASS.

- [ ] **Step 1.5: Commit**

```bash
cd /home/grant/local_code/slk && git add internal/emoji/shouldrender.go internal/emoji/shouldrender_test.go && git commit -m "feat(emoji): ShouldRenderUnicode classifier

Returns true for emoji strings that are exactly one base codepoint
or one base codepoint + VS16. Multi-codepoint sequences (ZWJ, flag
pairs, skin-tone modifiers) return false because terminal font
support is inconsistent and width disagreement breaks layout.

Centralizes the rule that future reaction-pill / picker / body-text
sites will share. See docs/superpowers/specs/2026-05-24-emoji-
shortcode-fallback-design.md."
```

---

## Task 2: Wire helper into message-pane reaction pills

**Files:**
- Modify: `internal/ui/messages/model.go:1635` (the reaction-pill loop)

The reaction pill currently does:
```go
emojiStr := emojiutil.Sprint(":" + emojiutil.StripSkinTone(r.Emoji) + ":")
pillText := fmt.Sprintf("%s%d", emojiStr, r.Count)
```

After: resolve via kyokomi, classify with `ShouldRenderUnicode`. If unsafe, fall back to the shortcode text.

- [ ] **Step 2.1: Read the surrounding context to confirm imports and helper names**

Run: `cd /home/grant/local_code/slk && rg -n 'emojiutil\.|kyoemoji\.|kyokomi/emoji' internal/ui/messages/model.go`

Expected: `emojiutil "github.com/gammons/slk/internal/emoji"` is already imported. No other kyokomi import; the kyokomi import was removed in the debug-instrumentation phase.

- [ ] **Step 2.2: Add the kyokomi import back**

Edit `internal/ui/messages/model.go` (top of import block, restoring the alias used elsewhere):

```go
import (
    // ... existing imports ...
    emojiutil "github.com/gammons/slk/internal/emoji"
    kyoemoji "github.com/kyokomi/emoji/v2"
    // ... other existing imports ...
)
```

Run: `cd /home/grant/local_code/slk && goimports -l -w internal/ui/messages/model.go || true`

- [ ] **Step 2.3: Change the reaction-pill resolution**

Locate the reaction-pill loop (around line 1635). Replace the two lines that build `emojiStr` and `pillText` with the per-emoji classifier:

```go
// Resolve the shortcode to Unicode and check whether the resolved
// form is composition-safe. Multi-codepoint sequences (ZWJ flags,
// skin tones, regional-indicator pairs) render as broken glyphs in
// many terminal fonts and break right-border alignment; in those
// cases we display the readable :shortcode: text instead. See
// internal/emoji/shouldrender.go for the rule.
nameForLookup := emojiutil.StripSkinTone(r.Emoji)
resolved := kyoemoji.Sprint(":" + nameForLookup + ":")
var emojiStr string
if emojiutil.ShouldRenderUnicode(resolved) {
    emojiStr = resolved
} else {
    emojiStr = ":" + nameForLookup + ":"
}
pillText := fmt.Sprintf("%s%d", emojiStr, r.Count)
```

- [ ] **Step 2.4: Run all messages tests**

Run: `cd /home/grant/local_code/slk && go test ./internal/ui/messages/...`
Expected: PASS. (TestViewLineWidths_* tests still pass because plain text widths are trivially consistent.)

- [ ] **Step 2.5: Build the whole project**

Run: `cd /home/grant/local_code/slk && go build ./...`
Expected: success, no output.

- [ ] **Step 2.6: Commit**

```bash
cd /home/grant/local_code/slk && git add internal/ui/messages/model.go && git commit -m "fix(reactions): fall back to :shortcode: for composition-fragile emoji

Message-pane reaction pills now route the resolved Unicode through
ShouldRenderUnicode. Multi-codepoint sequences (ZWJ flags like
:rainbow-flag:, skin-toned reactions, regional-indicator flag
pairs) display as readable :name: text instead of broken glyphs
that desync slk's right-border alignment.

Single-codepoint emoji and VS16-anchored emoji continue to render
as Unicode glyphs."
```

---

## Task 3: Apply same change to thread-pane reaction pills

**Files:**
- Modify: `internal/ui/thread/model.go:1595` (mirror of message-pane logic)

- [ ] **Step 3.1: Restore the kyokomi import in thread/model.go**

Run: `cd /home/grant/local_code/slk && rg -n 'kyokomi/emoji' internal/ui/thread/model.go`

If no match, add it to the import block (restoring the original alias used before the debug phase):

```go
import (
    // ... existing imports ...
    "github.com/gammons/slk/internal/debuglog"
    emojiutil "github.com/gammons/slk/internal/emoji"
    kyoemoji "github.com/kyokomi/emoji/v2"
    // ... other existing imports ...
)
```

- [ ] **Step 3.2: Update the reaction-pill resolution at line 1595**

Replace the existing two lines that build `emojiStr` and `pillText` with the same per-emoji classifier used in Task 2:

```go
nameForLookup := emojiutil.StripSkinTone(r.Emoji)
resolved := kyoemoji.Sprint(":" + nameForLookup + ":")
var emojiStr string
if emojiutil.ShouldRenderUnicode(resolved) {
    emojiStr = resolved
} else {
    emojiStr = ":" + nameForLookup + ":"
}
pillText := fmt.Sprintf("%s%d", emojiStr, r.Count)
```

- [ ] **Step 3.3: Run thread tests and build**

Run: `cd /home/grant/local_code/slk && go test ./internal/ui/thread/... && go build ./...`
Expected: PASS, then build success.

- [ ] **Step 3.4: Commit**

```bash
cd /home/grant/local_code/slk && git add internal/ui/thread/model.go && git commit -m "fix(thread): apply shortcode fallback to thread reaction pills

Mirror of the message-pane fix in the previous commit. Same
ShouldRenderUnicode rule for thread reactions."
```

---

## Task 4: Replace picker's len(runes)==1 rule with ShouldRenderUnicode

**Files:**
- Modify: `internal/ui/reactionpicker/model.go:50-67` (buildEmojiList — strip trailing space when storing Unicode)
- Modify: `internal/ui/reactionpicker/model.go:340-351` (the picker rendering check)

The picker's existing rule excludes ALL multi-codepoint emoji, which silently hides every colorful VS16-anchored emoji (`:heart:` ❤️, `:warning:` ⚠️, etc.). Switching to `ShouldRenderUnicode` will surface those.

A precondition: the picker currently stores the raw kyokomi `unicode` value in `EmojiEntry.Unicode`, which includes a trailing space (`"🌈 "`). `ShouldRenderUnicode` trims trailing whitespace defensively but it's cleaner to strip at load time so the stored value is the pure glyph.

- [ ] **Step 4.1: Add slkemoji import alias to reactionpicker**

Run: `cd /home/grant/local_code/slk && rg -n 'gammons/slk/internal/emoji' internal/ui/reactionpicker/model.go`

If absent, add to the import block:

```go
import (
    // ... existing imports ...
    slkemoji "github.com/gammons/slk/internal/emoji"
    // ... other existing ...
)
```

(slkemoji is the alias already used elsewhere in this file — verify with `rg slkemoji internal/ui/reactionpicker/model.go`. If a different alias is used, match it.)

- [ ] **Step 4.2: Strip trailing space when storing the built-in Unicode**

In `buildEmojiList` (around line 55), change:

```go
m.allEmoji = append(m.allEmoji, EmojiEntry{Name: name, Unicode: unicode})
```

to:

```go
m.allEmoji = append(m.allEmoji, EmojiEntry{Name: name, Unicode: strings.TrimRight(unicode, " ")})
```

(`strings` is already imported; verify.)

- [ ] **Step 4.3: Replace the picker rendering test**

In the row-rendering loop (around line 347), change:

```go
if len([]rune(entry.Unicode)) == 1 {
    line = entry.Unicode + " " + entry.Name
} else {
    line = ":" + entry.Name + ":"
}
```

to:

```go
// Use the shared composition-safety classifier instead of the
// stricter len(runes)==1 rule. The previous rule excluded all
// VS16-anchored colorful emoji (❤️, ⚠️, 🏳️ etc.) by accident.
if slkemoji.ShouldRenderUnicode(entry.Unicode) {
    line = entry.Unicode + " " + entry.Name
} else {
    line = ":" + entry.Name + ":"
}
```

- [ ] **Step 4.4: Update the explanatory comment block above the if/else**

Replace the existing comment (lines 340-345) with:

```go
// Display Unicode emoji when the resolved form is composition-safe
// (single base codepoint, optionally + VS16). Multi-codepoint
// sequences (ZWJ, regional-indicator flags, skin tones) render as
// broken glyphs in many terminal fonts and corrupt the picker's
// width arithmetic. See internal/emoji/shouldrender.go.
```

- [ ] **Step 4.5: Run picker tests + build**

Run: `cd /home/grant/local_code/slk && go test ./internal/ui/reactionpicker/... && go build ./...`
Expected: PASS, then build success.

- [ ] **Step 4.6: Commit**

```bash
cd /home/grant/local_code/slk && git add internal/ui/reactionpicker/model.go && git commit -m "fix(picker): use ShouldRenderUnicode (surfaces VS16 emoji)

Picker previously gated emoji display on len(runes)==1, which
correctly excluded ZWJ sequences and flag pairs but also
accidentally excluded every colorful VS16-anchored emoji (:heart:,
:warning:, :white_check_mark:, etc.). Users were seeing the
picker as a sea of black-and-white symbols.

Switching to ShouldRenderUnicode surfaces VS16 emoji as glyphs
while keeping the original protection against ZWJ/flag/skin-tone
sequences. Also trims kyokomi's trailing space when loading
built-ins so the stored Unicode is the pure glyph."
```

---

## Task 5: Body-text path — per-shortcode resolution

**Files:**
- Modify: `internal/ui/messages/render.go:561-565` (the emoji line in renderInlineFormatting)
- Create: `internal/emoji/render.go` (the per-shortcode resolution helper)
- Create: `internal/emoji/render_test.go`

The current body-text path calls `emoji.Sprint(text)` which scans `text` in one pass and substitutes every shortcode. To apply `ShouldRenderUnicode` per-shortcode we need a scanner that resolves each match individually.

- [ ] **Step 5.1: Write failing tests for the new helper**

Create `internal/emoji/render_test.go`:

```go
package emoji

import "testing"

func TestResolveShortcodesInText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"no shortcodes", "hello world", "hello world"},
		{"single safe emoji", "look :smile: here", "look 😄  here"},
		{"single unsafe (ZWJ) emoji stays text", "pride :rainbow_flag: month", "pride :rainbow_flag: month"},
		{"unsafe with slack hyphen form", "pride :rainbow-flag: month", "pride :rainbow-flag: month"},
		{"VS16 emoji renders", "love :heart: you", "love ❤️  you"},
		{"unknown shortcode passes through", "wat :nope: lol", "wat :nope: lol"},
		{"multiple safe", "hi :smile: and :fire:", "hi 😄  and 🔥 "},
		{"safe + unsafe mixed", "ok :smile: :rainbow_flag: done", "ok 😄  :rainbow_flag: done"},
		{"adjacent shortcodes", ":smile::fire:", "😄 🔥 "},
		{"empty", "", ""},
		{"colons only", "::", "::"},
		{"single colon", "a:b", "a:b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveShortcodesInText(c.input)
			if got != c.want {
				t.Errorf("ResolveShortcodesInText(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}
```

Note: kyokomi appends a trailing space to each resolved emoji (`":smile:"` → `"😄 "`). The expected strings include that space to match kyokomi's behavior, so body-text rendering before/after the change emits the same visible text for the safe cases. The unsafe cases leave the shortcode literally as it appeared in input.

- [ ] **Step 5.2: Run tests to verify they fail**

Run: `cd /home/grant/local_code/slk && go test ./internal/emoji -run TestResolveShortcodesInText -v`
Expected: FAIL with "undefined: ResolveShortcodesInText".

- [ ] **Step 5.3: Implement ResolveShortcodesInText**

Create `internal/emoji/render.go`:

```go
package emoji

import (
	"regexp"

	kyoemoji "github.com/kyokomi/emoji/v2"
)

// shortcodeRe matches Slack-style emoji shortcodes embedded in text:
// a colon, a name made of letters/digits/_/+/-, then a closing colon.
// Anchored by the colons; non-greedy isn't needed because the inner
// class disallows colons. Matches what Slack permits in custom emoji
// names plus the kyokomi built-in set.
var shortcodeRe = regexp.MustCompile(`:[A-Za-z0-9_+\-]+:`)

// ResolveShortcodesInText substitutes safe Slack-style :shortcode:
// sequences in s with their Unicode glyphs (matching kyokomi/emoji's
// trailing-space behavior for byte-for-byte parity with the previous
// emoji.Sprint(text) call). Shortcodes whose resolved Unicode form
// fails ShouldRenderUnicode (ZWJ sequences, flag pairs, skin-tone
// modifiers) are left as the literal :name: text so they render as
// readable shortcodes instead of broken glyphs.
//
// Unknown shortcodes pass through unchanged (kyokomi returns the
// input verbatim for unrecognized names).
func ResolveShortcodesInText(s string) string {
	return shortcodeRe.ReplaceAllStringFunc(s, func(match string) string {
		resolved := kyoemoji.Sprint(match)
		if resolved == match {
			// Unrecognized shortcode; pass through.
			return match
		}
		if ShouldRenderUnicode(resolved) {
			return resolved
		}
		return match
	})
}
```

- [ ] **Step 5.4: Run tests to verify they pass**

Run: `cd /home/grant/local_code/slk && go test ./internal/emoji -run TestResolveShortcodesInText -v`
Expected: All 12 sub-tests PASS.

- [ ] **Step 5.5: Update the body-text call site**

Edit `internal/ui/messages/render.go`. Around line 561-565, change:

```go
// Emoji shortcodes: :red_circle: -> 🔴
// Strip skin-tone modifier suffixes from shortcodes first; toned
// emoji render inconsistently across terminals and break alignment.
text = emojiutil.Sprint(emojiutil.StripSkinToneFromText(text))
```

to:

```go
// Emoji shortcodes: :red_circle: -> 🔴, but only when the resolved
// Unicode form is composition-safe (single codepoint or VS16-
// anchored). ZWJ sequences, flag pairs, and skin-tone modifiers
// stay as readable :shortcode: text — they break terminal
// width arithmetic on many fonts. See internal/emoji/shouldrender.go.
//
// StripSkinToneFromText runs first because skin-toned shortcodes
// (e.g. :wave_tone3:) should resolve as their base name (:wave:),
// not be left as literal text.
text = emojiutil.ResolveShortcodesInText(emojiutil.StripSkinToneFromText(text))
```

- [ ] **Step 5.6: Run messages tests + build**

Run: `cd /home/grant/local_code/slk && go test ./internal/ui/messages/... && go build ./...`
Expected: PASS, then build success.

- [ ] **Step 5.7: Commit**

```bash
cd /home/grant/local_code/slk && git add internal/emoji/render.go internal/emoji/render_test.go internal/ui/messages/render.go && git commit -m "fix(body): apply shortcode fallback to inline message-body emoji

Adds ResolveShortcodesInText that scans body text for :shortcode:
runs, resolves each via kyokomi, and substitutes the Unicode glyph
only when ShouldRenderUnicode passes. Multi-codepoint sequences
(ZWJ, flag pairs, skin-tone modifiers) embedded in message bodies
now render as readable :shortcode: text instead of broken glyphs.

Preserves kyokomi's trailing-space behavior for byte-for-byte
parity with the previous emoji.Sprint(text) call on the safe-path."
```

---

## Task 6: Cleanup — remove all debug instrumentation

**Files:**
- Delete: `internal/emoji/sprint.go` (SLK_NO_EMOJI env-var wrapper)
- Delete: `internal/emoji/test_helpers.go` (SetWidthMapForTest — only used by the debug test)
- Delete: `internal/ui/messages/width_repro_test.go` (debug test that never reproduced)
- Delete: `slk-frame-dump.txt` (one-off dump from the investigation)
- Modify: `internal/ui/messages/model.go` (remove debugCheckModal)
- Modify: `internal/ui/view_messages.go` (remove debugDumpLineWidths, dumpFrameIfRequested, per-stage logging)
- Modify: `internal/ui/sidebar/model.go` (revert emojiutil.Sprint → kyoemoji.Sprint with the imports it had)

- [ ] **Step 6.1: Delete debug files**

```bash
cd /home/grant/local_code/slk
rm internal/emoji/sprint.go
rm internal/emoji/test_helpers.go
rm internal/ui/messages/width_repro_test.go
rm -f slk-frame-dump.txt slk-debug.log
```

- [ ] **Step 6.2: Remove debugCheckModal from internal/ui/messages/model.go**

Search for the function:

```bash
cd /home/grant/local_code/slk && rg -n 'debugCheckModal' internal/ui/messages/model.go
```

Two hits expected: the function definition (around line 1289 region) and one or more call sites. Delete the function (the `func debugCheckModal(...)` block and its `// debugCheckModal ...` doc comment). Delete all call sites (`debugCheckModal("messages.View:...", visible)` lines).

After deletion:
- The `ansi.StringWidth` import in `messages/model.go` may still be needed for other code (the file already used it before debug instrumentation; verify with `rg ansi\. internal/ui/messages/model.go`).
- The `debuglog` import may no longer be needed in `messages/model.go` (it was added for the debug path). Verify with `rg debuglog internal/ui/messages/model.go`; if no other uses, remove the import.

- [ ] **Step 6.3: Revert internal/ui/view_messages.go**

The current file has:
- `dumpFrameIfRequested` function
- `debugDumpLineWidths` function
- Per-stage `debugDumpLineWidths(...)` calls inside `renderMessagesTop`
- `"os"`, `"strings"`, `"github.com/charmbracelet/x/ansi"`, `"github.com/gammons/slk/internal/debuglog"` imports added for the above

Restore to the pre-debug state. The file should look like:

```go
package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/styles"
)
```

(plus the existing top-of-file comment block — leave that untouched.)

And in `renderMessagesTop`, restore the body to:

```go
func (a *App) renderMessagesTop(msgWidth, msgBorder, topHeight, msgContentHeight int, topPanelVersion, topLayoutKey int64, msgFocused bool) string {
	c := &a.renderCache.msgTop
	if c.hit(topPanelVersion, msgWidth, topHeight, topLayoutKey) {
		return c.output
	}
	topBorderStyle := styles.UnfocusedBorder.Width(msgWidth).
		BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
	if msgFocused {
		topBorderStyle = styles.FocusedBorder.Width(msgWidth).
			BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
	}
	msgView := a.messagepane.View(msgContentHeight, msgWidth-2)
	msgView = messages.ReapplyBgAfterResets(msgView, messages.BgANSI())
	out := exactSize(
		topBorderStyle.Render(msgView),
		msgWidth+msgBorder, topHeight,
	)
	c.store(out, topPanelVersion, msgWidth, topHeight, topLayoutKey)
	return out
}
```

Delete the `dumpFrameIfRequested` and `debugDumpLineWidths` helpers entirely.

- [ ] **Step 6.4: Revert sidebar's emojiutil.Sprint to kyoemoji.Sprint**

In `internal/ui/sidebar/model.go`, around line 1348, the call was changed from `kyoemoji.Sprint(token)` to `emojiutil.Sprint(token)` during the debug phase. Revert it:

```go
rendered := kyoemoji.Sprint(token)
```

And restore the import block to include `kyoemoji "github.com/kyokomi/emoji/v2"` and remove the now-unused `emojiutil` import (verify it's not used elsewhere in the file with `rg emojiutil internal/ui/sidebar/model.go`).

(The sidebar section-header emojis use the kyokomi codemap directly and intentionally fall back to literal `:shortcode:` text when unknown. We don't apply ShouldRenderUnicode here because section headers are user-configured and out of scope for this fix. A future task can extend it if needed.)

- [ ] **Step 6.5: Build the whole project**

Run: `cd /home/grant/local_code/slk && go build ./...`
Expected: success, no output. If imports complain, run `goimports -w` on the modified files.

- [ ] **Step 6.6: Run the full test suite**

Run: `cd /home/grant/local_code/slk && go test ./...`
Expected: All tests PASS.

- [ ] **Step 6.7: Run linter**

Run: `cd /home/grant/local_code/slk && golangci-lint run ./internal/emoji/... ./internal/ui/messages/... ./internal/ui/thread/... ./internal/ui/reactionpicker/... ./internal/ui/sidebar/... ./internal/ui/view_messages.go`

Expected: no findings. If there are findings, fix them inline.

- [ ] **Step 6.8: Commit cleanup**

```bash
cd /home/grant/local_code/slk && git add -A && git commit -m "chore: remove emoji-width investigation debug artifacts

Reverts the temporary instrumentation added during the emoji-
width root-cause hunt:
- SLK_NO_EMOJI env-var passthrough wrapper (internal/emoji/sprint.go)
- SetWidthMapForTest helper (internal/emoji/test_helpers.go)
- width_repro_test.go (never reproduced; superseded by ShouldRenderUnicode tests)
- debugCheckModal + debugDumpLineWidths + dumpFrameIfRequested helpers
- per-stage line-width logging in renderMessagesTop
- frame dump and debug log files

Sidebar's section-header emoji call reverts to kyoemoji.Sprint
(same behavior as before the investigation)."
```

---

## Task 7: End-to-end verification

**Files:** none — manual smoke + final commit log review.

- [ ] **Step 7.1: Read the commit log**

Run: `cd /home/grant/local_code/slk && git log --oneline -10`

Expected: spec commit (`docs: spec — emoji shortcode fallback`), then six fix/cleanup commits in this order:
1. `feat(emoji): ShouldRenderUnicode classifier`
2. `fix(reactions): fall back to :shortcode: for composition-fragile emoji`
3. `fix(thread): apply shortcode fallback to thread reaction pills`
4. `fix(picker): use ShouldRenderUnicode (surfaces VS16 emoji)`
5. `fix(body): apply shortcode fallback to inline message-body emoji`
6. `chore: remove emoji-width investigation debug artifacts`

- [ ] **Step 7.2: Diff against `main` (or whatever the integration branch is)**

Run: `cd /home/grant/local_code/slk && git diff main --stat`

Expected: new files `internal/emoji/shouldrender.go`, `internal/emoji/shouldrender_test.go`, `internal/emoji/render.go`, `internal/emoji/render_test.go`, `docs/superpowers/specs/2026-05-24-emoji-shortcode-fallback-design.md`, `docs/superpowers/plans/2026-05-24-emoji-shortcode-fallback.md`. Modifications to `internal/ui/messages/model.go`, `internal/ui/messages/render.go`, `internal/ui/thread/model.go`, `internal/ui/reactionpicker/model.go`, `internal/ui/sidebar/model.go`. NO modifications to `internal/ui/view_messages.go` net of the debug additions (it should match `main`).

- [ ] **Step 7.3: User smoke test**

Hand off to the human partner. Manual checks:

1. Open the self-DM with the `:rainbow-flag:` reaction. The reaction pill should now show `:rainbow-flag: 1` (text). The right border on that row should align with surrounding rows.
2. Open any message with a single-codepoint reaction (`:fire:`, `:smile:`, etc.). The pill should still show the colorful emoji glyph followed by the count.
3. Open a message with a VS16 reaction (`:heart:`, `:warning:`). The pill should show the colorful emoji glyph.
4. Open the reaction picker on any message. Confirm VS16 emojis (`:heart:`, `:warning:`, `:white_check_mark:` etc.) are now visible as colorful glyphs (previously hidden as `:name:` text). ZWJ sequences (`:rainbow-flag:`, `:family:`) continue to show as shortcode text.
5. Confirm `SLK_DEBUG=1 slk` does NOT produce any `slk-frame-dump.txt` (the dumper was removed) and the debug log no longer contains `emoji-width` entries (instrumentation removed).

Once smoke checks pass, the plan is complete.
