# Accent-Insensitive Matching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make all ten fuzzy/substring matchers in slk match diacritics-insensitively (so `melanie` matches `Mélanie`, `cafe` matches `Café`, etc.) by introducing a single `text.Fold` primitive and routing every matcher's query and candidate strings through it.

**Architecture:** Add a tiny new package `internal/text` exposing one function `Fold(s string) string` that does NFD-decompose → strip combining marks (`unicode.Mn`) → NFC-recompose → `strings.ToLower`. Then mechanically swap `strings.ToLower(x)` → `text.Fold(x)` at the ten matcher call sites. Folding happens at match time only; no data-model changes; display strings are untouched.

**Tech Stack:** Go 1.26.1, `golang.org/x/text/unicode/norm`, `golang.org/x/text/transform`, `golang.org/x/text/runes` (new direct dependency on `golang.org/x/text`).

**Spec:** `docs/superpowers/specs/2026-05-19-accent-insensitive-matching-design.md`

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/text/fold.go` | The `Fold` primitive | Create |
| `internal/text/fold_test.go` | Table-driven unit tests for `Fold` | Create |
| `go.mod` / `go.sum` | Add `golang.org/x/text` direct dep | Modify (via `go mod tidy`) |
| `internal/ui/channelfinder/model.go` | Ctrl+t finder filter | Modify (lines 206, 228) |
| `internal/ui/channelfinder/model_test.go` | Accent integration test | Modify (append cases) |
| `internal/ui/mentionpicker/model.go` | @-picker filter | Modify (lines 109, 114, 121) |
| `internal/ui/mentionpicker/model_test.go` | Accent integration test | Modify (append cases) |
| `internal/ui/channelpicker/model.go` | #-picker filter | Modify (lines 123, 126) |
| `internal/ui/workspacefinder/model.go` | Ctrl+W finder filter | Modify (lines 115, 127) |
| `internal/ui/sidebar/model.go` | Sidebar filter | Modify (lines 785, 797) |
| `internal/ui/help/model.go` | `?` overlay search | Modify (lines 180, 183, 184) |
| `internal/ui/emojipicker/model.go` | `:foo:` picker | Modify (lines 91, 94) |
| `internal/ui/reactionpicker/model.go` | `r` picker | Modify (lines 141, 146, 148) |
| `internal/ui/themeswitcher/model.go` | Ctrl+Y switcher | Modify (lines 132, 143) |
| `internal/ui/presencemenu/model.go` | Ctrl+S menu | Modify (lines 173, 182) |

The new `internal/text` package is intentionally one-function. Future search-related helpers can live there. Call sites are listed by responsibility, not by technical layer.

---

## Task 1: Add `text.Fold` primitive (TDD)

**Files:**
- Create: `internal/text/fold.go`
- Create: `internal/text/fold_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Write the failing tests**

Write `internal/text/fold_test.go`:

```go
package text

import (
	"strings"
	"testing"
)

func TestFold(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ascii lower", "cafe", "cafe"},
		{"ascii mixed case", "Foo Bar", "foo bar"},
		{"french acute", "Mélanie", "melanie"},
		{"french acute upper", "MÉLANIE", "melanie"},
		{"french cedilla", "François", "francois"},
		{"french grave/acute mix", "Café", "cafe"},
		{"spanish tilde n", "año", "ano"},
		{"portuguese tilde a", "São", "sao"},
		{"german umlaut", "Müller", "muller"},
		{"vietnamese tones", "Việt", "viet"},
		{"mixed script (cjk passes through)", "東京 Tōkyō", "東京 tokyo"},
		{"already folded passes through", "melanie", "melanie"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Fold(tc.in)
			if got != tc.want {
				t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldMatchesBothDirections proves that folding makes substring matching
// symmetric across diacritics: query and candidate compare equal after fold
// regardless of which side carries the accents.
func TestFoldMatchesBothDirections(t *testing.T) {
	name := "Mélanie"
	for _, q := range []string{"melanie", "Mélanie", "MELANIE", "mél"} {
		if !strings.Contains(Fold(name), Fold(q)) {
			t.Errorf("Fold(%q) should contain Fold(%q)", name, q)
		}
	}
}

// TestFoldDoesNotPanicOnSingleCombiningMark guards against a regression
// where a lone combining mark (no base character) trips the transform.
func TestFoldDoesNotPanicOnSingleCombiningMark(t *testing.T) {
	// U+0301 COMBINING ACUTE ACCENT, in isolation.
	_ = Fold("\u0301")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/text/...`

Expected: build failure — `package github.com/gammons/slk/internal/text` does not exist yet (the test file is in a not-yet-created package).

- [ ] **Step 3: Add the `golang.org/x/text` dependency**

Run:

```bash
go get golang.org/x/text@latest
```

Expected: `go.mod` gains a `golang.org/x/text vX.Y.Z` line under `require (...)`, `go.sum` gains the corresponding checksums. Note the exact version chosen for the commit message.

- [ ] **Step 4: Create `internal/text/fold.go`**

Write `internal/text/fold.go`:

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
// transform.Chain itself is safe to reuse across calls because
// transform.String creates a fresh internal buffer per call.
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

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/text/... -v`

Expected: all tests PASS, including the 13 `TestFold` sub-tests, `TestFoldMatchesBothDirections`, and `TestFoldDoesNotPanicOnSingleCombiningMark`.

- [ ] **Step 6: Run `go mod tidy` to normalize**

Run: `go mod tidy`

Expected: no further changes to `go.mod` (the direct require for `golang.org/x/text` stays); `go.sum` may gain entries for transitive deps of `x/text` itself if any.

- [ ] **Step 7: Commit**

```bash
git add internal/text/fold.go internal/text/fold_test.go go.mod go.sum
git commit -m "text: add Fold for case- and accent-insensitive matching"
```

---

## Task 2: Use `text.Fold` in the Ctrl+t channel finder (TDD)

**Files:**
- Modify: `internal/ui/channelfinder/model.go:206,228`
- Modify: `internal/ui/channelfinder/model_test.go` (append cases)

- [ ] **Step 1: Write the failing test (append to existing file)**

Append to `internal/ui/channelfinder/model_test.go`:

```go
// TestFilterAccentInsensitive proves that an ASCII query matches a
// candidate that has diacritics (issue #14). Drives m.query directly
// rather than via HandleKey, because HandleKey currently restricts input
// to single-byte printable ASCII (model.go:179) — that input-side
// limitation is a separate concern from the matching behavior under test.
func TestFilterAccentInsensitive(t *testing.T) {
	items := []Item{
		{ID: "D1", Name: "Mélanie", Type: "dm", LastVisited: 100},
		{ID: "D2", Name: "François", Type: "dm", LastVisited: 90},
		{ID: "C1", Name: "café-discussion", Type: "channel", LastVisited: 80},
	}
	cases := []struct {
		query   string
		wantID  string
		comment string
	}{
		{"melanie", "D1", "prefix match without accents"},
		{"francois", "D2", "substring match without cedilla"},
		{"cafe", "C1", "prefix match without acute"},
		{"Mélanie", "D1", "accented query also matches accented candidate"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.query, func(t *testing.T) {
			m := New()
			m.SetItems(items)
			m.Open()
			m.query = tc.query
			m.filter()
			if len(m.filtered) == 0 {
				t.Fatalf("expected at least 1 match for %q (%s)", tc.query, tc.comment)
			}
			gotID := m.items[m.filtered[0]].ID
			if gotID != tc.wantID {
				t.Errorf("query %q: expected first match %s, got %s (%s)",
					tc.query, tc.wantID, gotID, tc.comment)
			}
		})
	}
}
```

Note: the test is in the same package as `Model`, so it can write `m.query` and call `m.filter()` directly. Existing tests in this file already access the unexported `m.filtered` and `m.items` fields (lines 28, 45, 48).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ui/channelfinder/ -run TestFilterAccentInsensitive -v`

Expected: at least the `melanie`, `francois`, and `cafe` sub-tests FAIL — the current `strings.ToLower` + `strings.Contains` pipeline cannot match across diacritics. The `Mélanie` sub-test may pass (exact match) but that is irrelevant; we need the other three to fail.

- [ ] **Step 3: Swap `strings.ToLower` → `text.Fold` in `filter()`**

Read `internal/ui/channelfinder/model.go` lines 200-240 to confirm the exact current text. Then make two edits:

Add the import (the file already imports `"strings"`, `"sort"`; add the new one in the same import block, keeping alphabetical order):

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 206:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 228 (inside the `for i, item := range m.items` loop):

```go
// Before:
		name := strings.ToLower(item.Name)
// After:
		name := text.Fold(item.Name)
```

Leave the line-265 `strings.ToLower(a.Name) < strings.ToLower(b.Name)` tiebreaker comparison alone — that's a display-order tiebreak inside a single sort, not a match decision, and accent-folding it would just reorder ties in a way the user doesn't see.

- [ ] **Step 4: Run the new test to verify it now passes**

Run: `go test ./internal/ui/channelfinder/ -run TestFilterAccentInsensitive -v`

Expected: all four sub-tests PASS.

- [ ] **Step 5: Run the full channelfinder package tests to verify no regressions**

Run: `go test ./internal/ui/channelfinder/ -v`

Expected: all existing tests still PASS (none of them use diacritics, so behavior for ASCII inputs is preserved).

- [ ] **Step 6: Commit**

```bash
git add internal/ui/channelfinder/model.go internal/ui/channelfinder/model_test.go
git commit -m "channelfinder: use text.Fold for accent-insensitive Ctrl+t matching"
```

---

## Task 3: Use `text.Fold` in the @-mention picker (TDD)

**Files:**
- Modify: `internal/ui/mentionpicker/model.go:109,114,121`
- Modify: `internal/ui/mentionpicker/model_test.go` (append cases)

- [ ] **Step 1: Write the failing test (append to existing file)**

Append to `internal/ui/mentionpicker/model_test.go`:

```go
func TestFilterAccentInsensitive(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "François", Username: "francois.b"},
		{ID: "U2", DisplayName: "Mélanie", Username: "melanie"},
		{ID: "U3", DisplayName: "Alice", Username: "alice"},
	})
	cases := []struct {
		query  string
		wantID string
	}{
		{"francois", "U1"},
		{"melanie", "U2"},
		{"François", "U1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.query, func(t *testing.T) {
			m.Open()
			m.SetQuery(tc.query)
			if len(m.Filtered()) == 0 {
				t.Fatalf("expected at least 1 match for %q", tc.query)
			}
			if m.Filtered()[0].ID != tc.wantID {
				t.Errorf("query %q: expected %s, got %s",
					tc.query, tc.wantID, m.Filtered()[0].ID)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ui/mentionpicker/ -run TestFilterAccentInsensitive -v`

Expected: the `francois` and `melanie` sub-tests FAIL (current matcher requires the accents).

- [ ] **Step 3: Swap `strings.ToLower` → `text.Fold` in `filter()`**

Add the import to `internal/ui/mentionpicker/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 109:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 114 (inside the special-mentions loop):

```go
// Before:
		if q == "" || strings.HasPrefix(strings.ToLower(u.DisplayName), q) || strings.HasPrefix(strings.ToLower(u.Username), q) {
// After:
		if q == "" || strings.HasPrefix(text.Fold(u.DisplayName), q) || strings.HasPrefix(text.Fold(u.Username), q) {
```

Edit line 121 (inside the regular-users loop) — same replacement:

```go
// Before:
		if q == "" || strings.HasPrefix(strings.ToLower(u.DisplayName), q) || strings.HasPrefix(strings.ToLower(u.Username), q) {
// After:
		if q == "" || strings.HasPrefix(text.Fold(u.DisplayName), q) || strings.HasPrefix(text.Fold(u.Username), q) {
```

The `"strings"` import stays (used for `strings.HasPrefix`).

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/ui/mentionpicker/ -run TestFilterAccentInsensitive -v`

Expected: all three sub-tests PASS.

- [ ] **Step 5: Run the full mentionpicker package tests**

Run: `go test ./internal/ui/mentionpicker/ -v`

Expected: all existing tests still PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/mentionpicker/model.go internal/ui/mentionpicker/model_test.go
git commit -m "mentionpicker: use text.Fold for accent-insensitive @-mention matching"
```

---

## Task 4: Use `text.Fold` in the #-channel picker

**Files:**
- Modify: `internal/ui/channelpicker/model.go:123,126`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/channelpicker/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 123:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 126:

```go
// Before:
		if q == "" || strings.HasPrefix(strings.ToLower(c.Name), q) {
// After:
		if q == "" || strings.HasPrefix(text.Fold(c.Name), q) {
```

- [ ] **Step 2: Build to verify the package still compiles**

Run: `go build ./internal/ui/channelpicker/...`

Expected: success, no output.

- [ ] **Step 3: Run the channelpicker package tests**

Run: `go test ./internal/ui/channelpicker/...`

Expected: all PASS. (No new test added — `text.Fold` behavior is covered by `internal/text/fold_test.go`, and the channelpicker logic is a one-line wrapper.)

- [ ] **Step 4: Commit**

```bash
git add internal/ui/channelpicker/model.go
git commit -m "channelpicker: use text.Fold for accent-insensitive #-picker matching"
```

---

## Task 5: Use `text.Fold` in the Ctrl+W workspace finder

**Files:**
- Modify: `internal/ui/workspacefinder/model.go:115,127`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/workspacefinder/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 115:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 127 (inside the `for i, item := range m.items` loop):

```go
// Before:
		name := strings.ToLower(item.Name)
// After:
		name := text.Fold(item.Name)
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/workspacefinder/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/workspacefinder/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/workspacefinder/model.go
git commit -m "workspacefinder: use text.Fold for accent-insensitive workspace matching"
```

---

## Task 6: Use `text.Fold` in the sidebar channel filter

**Files:**
- Modify: `internal/ui/sidebar/model.go:785,797`

- [ ] **Step 1: Edit `rebuildFilter()`**

Add the import to `internal/ui/sidebar/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 785:

```go
// Before:
	lower := strings.ToLower(m.filter)
// After:
	lower := text.Fold(m.filter)
```

Edit line 797:

```go
// Before:
		if m.filter != "" && !strings.Contains(strings.ToLower(item.Name), lower) {
// After:
		if m.filter != "" && !strings.Contains(text.Fold(item.Name), lower) {
```

Note: the local variable name `lower` is now mildly inaccurate (it's folded, not just lowered) but renaming it would balloon the diff across this large file. Leave the name; the call-site change is what matters.

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/sidebar/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/sidebar/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/sidebar/model.go
git commit -m "sidebar: use text.Fold for accent-insensitive channel filter"
```

---

## Task 7: Use `text.Fold` in the `?` help overlay search

**Files:**
- Modify: `internal/ui/help/model.go:180,183,184`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/help/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 180:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit lines 183-184:

```go
// Before:
		if q == "" ||
			strings.Contains(strings.ToLower(e.Desc), q) ||
			strings.Contains(strings.ToLower(e.Key), q) {
// After:
		if q == "" ||
			strings.Contains(text.Fold(e.Desc), q) ||
			strings.Contains(text.Fold(e.Key), q) {
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/help/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/help/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/help/model.go
git commit -m "help: use text.Fold for accent-insensitive help search"
```

---

## Task 8: Use `text.Fold` in the inline emoji picker

**Files:**
- Modify: `internal/ui/emojipicker/model.go:91,94`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/emojipicker/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 91:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 94:

```go
// Before:
		if q == "" || strings.HasPrefix(strings.ToLower(e.Name), q) {
// After:
		if q == "" || strings.HasPrefix(text.Fold(e.Name), q) {
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/emojipicker/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/emojipicker/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/emojipicker/model.go
git commit -m "emojipicker: use text.Fold for consistency with other matchers"
```

---

## Task 9: Use `text.Fold` in the reaction picker

**Files:**
- Modify: `internal/ui/reactionpicker/model.go:141,146,148`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/reactionpicker/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 141:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit lines 146 and 148 — note these currently do NOT lowercase `e.Name`, so wrapping with `text.Fold` also fixes a latent case-sensitivity bug:

```go
// Before:
		if strings.HasPrefix(e.Name, q) {
			m.filtered = append(m.filtered, e)
		} else if strings.Contains(e.Name, q) {
// After:
		if strings.HasPrefix(text.Fold(e.Name), q) {
			m.filtered = append(m.filtered, e)
		} else if strings.Contains(text.Fold(e.Name), q) {
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/reactionpicker/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/reactionpicker/...`

Expected: all PASS. If any existing test assumes case-sensitive behavior of the reaction picker, it will fail here — read the failure carefully. The expected fix in that case is to update the test, because the new behavior (case-insensitive) matches every other picker in the app and matches the picker's own `strings.ToLower(m.query)` intent on line 141.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/reactionpicker/model.go
git commit -m "reactionpicker: use text.Fold (also fixes latent case-sensitivity)"
```

---

## Task 10: Use `text.Fold` in the theme switcher

**Files:**
- Modify: `internal/ui/themeswitcher/model.go:132,143`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/themeswitcher/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 132:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 143 (inside the `for i, item := range m.items` loop):

```go
// Before:
		name := strings.ToLower(item)
// After:
		name := text.Fold(item)
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/themeswitcher/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/themeswitcher/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/themeswitcher/model.go
git commit -m "themeswitcher: use text.Fold for accent-insensitive theme matching"
```

---

## Task 11: Use `text.Fold` in the presence menu

**Files:**
- Modify: `internal/ui/presencemenu/model.go:173,182`

- [ ] **Step 1: Edit `filter()`**

Add the import to `internal/ui/presencemenu/model.go`:

```go
import (
	// existing imports...
	"github.com/gammons/slk/internal/text"
)
```

Edit line 173:

```go
// Before:
	q := strings.ToLower(m.query)
// After:
	q := text.Fold(m.query)
```

Edit line 182 (inside the `for i, it := range m.items` loop):

```go
// Before:
		name := strings.ToLower(it.label)
// After:
		name := text.Fold(it.label)
```

- [ ] **Step 2: Build**

Run: `go build ./internal/ui/presencemenu/...`

Expected: success.

- [ ] **Step 3: Test**

Run: `go test ./internal/ui/presencemenu/...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/presencemenu/model.go
git commit -m "presencemenu: use text.Fold for accent-insensitive presence matching"
```

---

## Task 12: Full-repo verification

**Files:** none

- [ ] **Step 1: Build the whole repo**

Run: `make build`

Expected: a fresh `bin/slk` is produced with no errors or warnings.

- [ ] **Step 2: Run the full test suite with `-race`**

Run: `make test`

Expected: all packages PASS, no race detector warnings. If `golangci-lint` is configured in the dev environment, also run `make lint` and address any new findings in the touched files (most likely just import ordering).

- [ ] **Step 3: Confirm no `strings.ToLower(` calls remain in the ten filter functions**

Run:

```bash
grep -nE 'strings\.ToLower\(' \
  internal/ui/channelfinder/model.go \
  internal/ui/mentionpicker/model.go \
  internal/ui/channelpicker/model.go \
  internal/ui/workspacefinder/model.go \
  internal/ui/sidebar/model.go \
  internal/ui/help/model.go \
  internal/ui/emojipicker/model.go \
  internal/ui/reactionpicker/model.go \
  internal/ui/themeswitcher/model.go \
  internal/ui/presencemenu/model.go
```

Expected: the only `strings.ToLower` remaining inside any matcher's `filter()` should be the display-order tiebreaker at `internal/ui/channelfinder/model.go:265` (the `strings.ToLower(a.Name) < strings.ToLower(b.Name)` sort comparator deliberately left in place). Other `strings.ToLower` usages elsewhere in these files (outside the matching path — e.g., name comparisons used for sorting other things) are also acceptable. If anything inside a `filter()` function still calls `strings.ToLower` on a query or candidate, go back to that task and fix it.

- [ ] **Step 4: Manual smoke (optional but recommended)**

Run: `./bin/slk` against a real workspace, open the Ctrl+t finder, and type `melanie` (or another unaccented form of a user/channel name you know has diacritics). Confirm the accented entry appears in the results list.

- [ ] **Step 5: Push the branch and open a PR**

```bash
git push -u origin HEAD
gh pr create --fill --body "Closes #14.

Adds internal/text.Fold for case- and accent-insensitive matching, then routes
the ten existing fuzzy/substring matchers through it. See
docs/superpowers/specs/2026-05-19-accent-insensitive-matching-design.md and
docs/superpowers/plans/2026-05-19-accent-insensitive-matching.md."
```
