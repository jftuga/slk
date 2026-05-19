# Accent-Insensitive Matching Design

Closes [#14](https://github.com/gammons/slk/issues/14).

## Problem

Every fuzzy/substring matcher in slk is diacritic-sensitive. Typing
`melanie` in the Ctrl+t switcher does not match `Mélanie`, `cafe` does not
match `Café`, `francois` does not match `François`. For users in locales
where accented names and channels are common (French, Spanish, Portuguese,
German, Vietnamese, …), this means either remembering which characters are
accented and typing the right key combo, or never finding a contact on the
first try.

Today the codebase has ten matcher sites, all using the same
`strings.ToLower(...)` + `strings.Contains`/`HasPrefix` pattern (plus one
custom subsequence scorer in the Ctrl+t finder). None of them perform any
Unicode normalization.

## Solution

Introduce a single tiny primitive — `text.Fold(s string) string` — that
returns a case- and diacritic-folded version of its input, then route
every matcher's query and candidate strings through it at the ten matcher
sites. In nine of those sites this is a direct `strings.ToLower(x)` →
`text.Fold(x)` swap; the tenth (reaction picker) currently skips
lowercasing on one side, so the change also folds that side for the first
time, fixing a latent case-sensitivity bug as a side effect. Display
strings are never mutated; folding happens only at match time.

Result: `text.Fold("Mélanie") == text.Fold("melanie") == "melanie"`, so
`strings.Contains(text.Fold(name), text.Fold(query))` matches in either
direction. Exact accented input (`Mélanie`) still matches, since both sides
are folded consistently.

## New Package: `internal/text`

`internal/text/fold.go`:

```go
// Package text provides string utilities for search and matching.
package text

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// foldChain decomposes (NFD), strips combining marks, then recomposes (NFC).
var foldChain = transform.Chain(
	norm.NFD,
	runes.Remove(runes.In(unicode.Mn)),
	norm.NFC,
)

// Fold returns s with diacritics removed and lowercased, suitable for
// case- and accent-insensitive substring matching.
//
//	Fold("Mélanie")  == "melanie"
//	Fold("François") == "francois"
//	Fold("Café")     == "cafe"
//
// Characters with no decomposition (CJK, emoji, plain ASCII) pass through
// unchanged apart from lowercasing. If the transform pipeline ever returns
// an error (it should not for in-memory strings), Fold falls back to
// strings.ToLower(s) so matching degrades gracefully instead of panicking.
func Fold(s string) string {
	out, _, err := transform.String(foldChain, s)
	if err != nil {
		return strings.ToLower(s)
	}
	return strings.ToLower(out)
}
```

The package is intentionally small — one exported function with one job.
Future search-related helpers (e.g. an extracted tiered scorer) can live
here, but that is out of scope for this issue.

## Dependency

Adds `golang.org/x/text` (BSD-licensed, maintained by the Go team, already
ubiquitous in the Go ecosystem) as a direct module dependency. The three
sub-packages used are `unicode/norm`, `transform`, and `runes`. Go 1.26.1
(the repo's current `go` directive) supports all of them.

## Call-Site Changes

Mechanical swap of `strings.ToLower(x)` → `text.Fold(x)` at the ten matcher
sites identified during exploration. Both the query and the candidate are
folded; display strings remain unchanged.

| # | File:line | Site | Notes |
|---|---|---|---|
| 1 | `internal/ui/channelfinder/model.go:206,228` | Ctrl+t channel finder | The custom `subsequenceScore` at line 290 is fed pre-folded inputs and needs no internal change. |
| 2 | `internal/ui/mentionpicker/model.go:114,121` | @-mention picker | Fold query once; fold `DisplayName`/`Username` per item. |
| 3 | `internal/ui/channelpicker/model.go:126` | #-channel picker | Fold query + `c.Name`. |
| 4 | `internal/ui/workspacefinder/model.go:128,130` | Ctrl+W workspace finder | Fold query + `item.Name`. |
| 5 | `internal/ui/sidebar/model.go:785,797` | Sidebar channel filter | Fold `m.filter` + `item.Name`. |
| 6 | `internal/ui/help/model.go:180,183,184` | `?` help overlay search | Fold query + `e.Desc` + `e.Key`. |
| 7 | `internal/ui/emojipicker/model.go:91,94` | `:foo:` inline emoji picker | Emoji names are ASCII today; consistency is cheap. |
| 8 | `internal/ui/reactionpicker/model.go:141,146,148` | `r` reaction picker | This site currently does NOT lowercase `e.Name` — using `text.Fold` on both sides fixes that latent bug as a side effect. |
| 9 | `internal/ui/themeswitcher/model.go:132,143-147` | Ctrl+Y theme switcher | Fold query + theme name. |
| 10 | `internal/ui/presencemenu/model.go:173,182-186` | Ctrl+S presence menu | Fold query + presence label. |

No data-model changes, no struct fields added, no migrations. Behavior for
ASCII-only inputs is identical to today (`Fold` of an ASCII string is just
`strings.ToLower`).

## Folding Timing

Fold at match time only. The filter functions already iterate all items on
every keystroke; we add one `text.Fold` per item per keystroke. For a TUI
with O(thousands) of items and O(tens of millis) per keystroke this is fine
and avoids any cache-invalidation invariants on item populate paths. If
profiling later shows it is hot, the natural follow-up is to add a cached
`foldedName` field on the picker item structs — but we defer that until
there is evidence the cost matters.

## Testing

`internal/text/fold_test.go` — table-driven unit tests:

- French: `Mélanie` / `melanie` / `MÉLANIE` all fold to `melanie`.
- Cross-direction: `strings.Contains(Fold("Mélanie"), Fold("melanie"))` and
  `strings.Contains(Fold("melanie"), Fold("Mélanie"))` both true.
- Spanish/Portuguese: `año` → `ano`, `São` → `sao`.
- German umlauts: `Müller` → `muller`. (See "Out of Scope" re: `ß`.)
- Vietnamese tones (multiple combining marks per glyph): `Việt` → `viet`.
- ASCII passthrough: `Foo Bar` → `foo bar`, `cafe` → `cafe`.
- Empty string, single combining mark in isolation, mixed-script.

Plus targeted integration tests added to the two existing test files for
the largest matcher sites:

- `internal/ui/channelfinder/model_test.go` — add cases proving an item
  named `Mélanie` is matched by query `melanie` across all three tiers
  (prefix, substring, subsequence).
- `internal/ui/mentionpicker/model_test.go` — add cases proving a user
  with `DisplayName: "François"` is matched by query `francois`.

The other eight sites are mechanical one-line swaps with no behavioral
risk and are covered by the package-level `fold_test.go`.

## Out of Scope

- Locale-specific folding (Turkish `İ` ↔ `i`/`ı`, German `ß` → `ss`).
  Adding `golang.org/x/text/cases` would handle these but is materially
  heavier; defer until someone files a follow-up.
- Refactoring the channelfinder's three-tier prefix/substring/subsequence
  scorer into a reusable Matcher type.
- Persisting a `folded_name` column in the SQLite cache.
- Touching the on-the-wire Slack data, `wctx.UserNames` population, or any
  Slack API call.
- Pre-computed `foldedName` fields on item structs.

## Risks and Mitigations

- **New module dependency (`golang.org/x/text`).** Mitigation: it is a
  Go-team module under the same governance as the standard library, used
  by most non-trivial Go projects; the three sub-packages we import are
  stable and have no further transitive deps beyond `golang.org/x/text`
  itself.
- **`Fold` returns an error fallback path.** `transform.String` over an
  in-memory string with these specific transformers cannot fail in
  practice, but the error path returns `strings.ToLower(s)` so callers
  cannot regress past today's behavior even in a pathological case.
- **`ß` does not fold to `ss`.** Documented limitation. German users today
  cannot match `Straße` by typing `strasse` either, so this is not a
  regression.
