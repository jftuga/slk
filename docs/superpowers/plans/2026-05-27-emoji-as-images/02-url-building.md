# Phase 2: URL Building

> Index: `00-overview.md`. Previous: `01-config.md`. Next: `03-token-model.md`.
>
> **Correction (post-implementation):** This plan claimed VS16 (`U+FE0F`)
> should be STRIPPED from URL paths. That was wrong. Slack's CDN preserves
> every codepoint: `2764.png` returns 403 and `2764-fe0f.png` returns the
> heart image. The implementation in `internal/emoji/url.go` and the fixture
> at `internal/emoji/testdata/slack_urls.json` have been corrected. Treat
> any mention of "VS16 stripped" below as historical-incorrect; the live
> behavior is "VS16 preserved" (matching every other codepoint). The spec
> at `docs/superpowers/specs/2026-05-27-emoji-as-images-design.md` is the
> updated source of truth.

**Goal:** A pure-function library at `internal/emoji/url.go` that converts:
- A Unicode codepoint sequence (e.g., `[0x1F44D]`, `[0x1F468, 0x200D, 0x1F680]`) into a Slack CDN URL.
- A shortcode name (e.g., `"thumbsup"`, `"party_parrot"`) into a Slack CDN URL, using the workspace customs map for non-kyokomi names, with alias-chain resolution.

VS16 (`U+FE0F`) is stripped during URL construction (matches Slack web's convention). ZWJ (`U+200D`) and regional-indicator codepoints are preserved.

A captured-fixture file (`testdata/slack_urls.json`) validates the URL builder against real Slack-served URLs.

**Files:**
- Create: `internal/emoji/url.go`
- Create: `internal/emoji/url_test.go`
- Create: `internal/emoji/testdata/slack_urls.json`

---

### Task 2.1: Create the fixture file

**Files:**
- Create: `internal/emoji/testdata/slack_urls.json`

**Why:** Real Slack URLs are the source of truth for the VS16-stripping rule. The fixture lets the tests assert against real-world data, not guesses. It also documents the convention so future changes can't silently drift.

- [ ] **Step 1: Create the testdata directory**

```bash
mkdir -p internal/emoji/testdata
```

- [ ] **Step 2: Seed the fixture with documented Slack convention**

Create `internal/emoji/testdata/slack_urls.json`:

```json
{
  "_comment": "Captured/expected Slack CDN URLs for representative emoji, used to validate internal/emoji/url.go. To re-capture: open Slack web with a workspace using the 'google' emoji style, open browser network tab filtered to slack-edge.com, expand a message containing the emoji, and copy the URL of the .png request. VS16 (U+FE0F) is stripped from URL paths; ZWJ (U+200D) and regional-indicator codepoints are kept.",
  "base": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/",
  "entries": [
    {"name": "thumbsup",      "codepoints": [128077],                 "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f44d.png"},
    {"name": "heart",         "codepoints": [10084, 65039],           "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/2764.png"},
    {"name": "warning",       "codepoints": [9888, 65039],            "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/26a0.png"},
    {"name": "joy",           "codepoints": [128514],                 "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f602.png"},
    {"name": "man_astronaut", "codepoints": [128104, 8205, 128640],   "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f468-200d-1f680.png"},
    {"name": "rainbow-flag",  "codepoints": [127987, 65039, 8205, 127752], "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f3f3-200d-1f308.png"},
    {"name": "us",            "codepoints": [127482, 127480],         "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f1fa-1f1f8.png"},
    {"name": "thumbsup_tone3","codepoints": [128077, 127997],         "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f44d-1f3fd.png"},
    {"name": "fire",          "codepoints": [128293],                 "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f525.png"},
    {"name": "eyes",          "codepoints": [128064],                 "url": "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/1f440.png"}
  ]
}
```

(The codepoint integers above are decimal; the hex equivalents land in the URL. `127149` = `0x1F44D`; `10084` = `0x2764`; `65039` = `0xFE0F` (VS16, stripped); `8205` = `0x200D` (ZWJ, kept). The decimal-in-JSON form is what `json.Unmarshal` produces for `[]int`/`[]rune`.)

- [ ] **Step 3: Re-capture verification (optional but recommended)**

The decimal codepoints above are derived from the kyokomi codemap; the URL strings are the documented Slack CDN convention. If at any point during this phase a URL test fails, the developer should open Slack web (workspace using the "google" emoji style), open the browser's network tab filtered to `slack-edge.com`, view a message containing the named emoji, and copy the actual URL. Replace the entry in `slack_urls.json` and re-run the test. The URL builder, not the fixture, should accommodate Slack's behavior.

- [ ] **Step 4: Commit**

```bash
git add internal/emoji/testdata/slack_urls.json
git commit -m "test(emoji): seed Slack CDN URL fixtures for URL-builder tests"
```

---

### Task 2.2: Failing test for `BuildStandardEmojiURL`

**Files:**
- Create: `internal/emoji/url_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/emoji/url_test.go`:

```go
package emoji

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type urlFixture struct {
	Base    string         `json:"base"`
	Entries []urlFixtureEntry `json:"entries"`
}

type urlFixtureEntry struct {
	Name       string `json:"name"`
	Codepoints []rune `json:"codepoints"`
	URL        string `json:"url"`
}

func loadURLFixture(t *testing.T) urlFixture {
	t.Helper()
	path := filepath.Join("testdata", "slack_urls.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f urlFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Entries) == 0 {
		t.Fatalf("fixture has no entries")
	}
	return f
}

func TestBuildStandardEmojiURL(t *testing.T) {
	fixture := loadURLFixture(t)
	for _, e := range fixture.Entries {
		got := BuildStandardEmojiURL(e.Codepoints)
		if got != e.URL {
			t.Errorf("BuildStandardEmojiURL(%q codepoints=%v) = %q, want %q",
				e.Name, e.Codepoints, got, e.URL)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestBuildStandardEmojiURL -v`
Expected: FAIL — `BuildStandardEmojiURL` is undefined (compile error).

---

### Task 2.3: Implement `BuildStandardEmojiURL`

**Files:**
- Create: `internal/emoji/url.go`

- [ ] **Step 1: Write the implementation**

Create `internal/emoji/url.go`:

```go
package emoji

import (
	"fmt"
	"strings"
)

// CDNBaseURL is the prefix Slack uses for its standard-emoji asset
// images. The "16.0" version segment changes when Slack ships a new
// emoji generation; if a future asset reorganization breaks our URLs,
// updating this constant is the single edit needed.
//
// "google-small" is Slack's Google-style emoji set at the smallest
// pre-rendered size (~16x16px). Workspace admins on Slack web can
// pick between Apple / Google / Twitter / Slack-classic; v1 of this
// renderer hardcodes the google set, matching the default for most
// workspaces. Per-workspace style detection is a follow-up.
const CDNBaseURL = "https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/"

// vs16 is U+FE0F, the variation selector that requests emoji
// presentation for a base codepoint. Slack web strips it from URL
// paths (e.g., :heart: at U+2764 U+FE0F serves as "2764.png", not
// "2764-fe0f.png"). ZWJ (U+200D) sequences and regional-indicator
// flag pairs are preserved.
const vs16 = '\uFE0F'

// BuildStandardEmojiURL returns the Slack CDN URL for a standard
// (kyokomi-known or Unicode-property-detected) emoji's codepoint
// sequence. Codepoints are lowercase-hex, dash-joined; U+FE0F is
// stripped.
//
// Returns "" if codepoints is empty.
func BuildStandardEmojiURL(codepoints []rune) string {
	parts := make([]string, 0, len(codepoints))
	for _, r := range codepoints {
		if r == vs16 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%x", r))
	}
	if len(parts) == 0 {
		return ""
	}
	return CDNBaseURL + strings.Join(parts, "-") + ".png"
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/emoji/ -run TestBuildStandardEmojiURL -v`
Expected: PASS for all fixture entries.

If any single entry fails, the captured URL in `slack_urls.json` is the source of truth — investigate whether Slack's actual behavior differs from the VS16-strip-only rule. Common alternative behaviors to check: (a) Slack also strips `U+200D` in some sequences (unlikely but possible), (b) Slack lowercases differently for high codepoints (uppercase observed historically in some legacy paths).

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/url.go internal/emoji/url_test.go
git commit -m "feat(emoji): BuildStandardEmojiURL for Slack CDN URLs from codepoints"
```

---

### Task 2.4: Failing test for `CodepointsForShortcode`

**Files:**
- Modify: `internal/emoji/url_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/emoji/url_test.go`:

```go
func TestCodepointsForShortcode_Builtin(t *testing.T) {
	cases := []struct {
		name string
		want []rune // expected codepoints
	}{
		{"thumbsup", []rune{0x1F44D}},
		{"heart", []rune{0x2764, 0xFE0F}},
		{"man_astronaut", []rune{0x1F468, 0x200D, 0x1F680}},
		{"warning", []rune{0x26A0, 0xFE0F}},
		{"fire", []rune{0x1F525}},
	}
	for _, c := range cases {
		got, ok := CodepointsForShortcode(c.name)
		if !ok {
			t.Errorf("CodepointsForShortcode(%q): ok=false, want a kyokomi hit", c.name)
			continue
		}
		if !runesEqual(got, c.want) {
			t.Errorf("CodepointsForShortcode(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCodepointsForShortcode_Unknown(t *testing.T) {
	if _, ok := CodepointsForShortcode("definitely_not_an_emoji_name_xyz"); ok {
		t.Errorf("CodepointsForShortcode(unknown): ok=true, want false")
	}
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestCodepointsForShortcode -v`
Expected: FAIL — `CodepointsForShortcode` is undefined.

---

### Task 2.5: Implement `CodepointsForShortcode`

**Files:**
- Modify: `internal/emoji/url.go`

- [ ] **Step 1: Add the helper**

Append to `internal/emoji/url.go`:

```go
import emojilib "github.com/kyokomi/emoji/v2"
```

(Merge into the existing import block at the top of the file.)

Then append:

```go
// CodepointsForShortcode resolves a Slack-style shortcode name (no
// colons) to its Unicode codepoint sequence using the kyokomi
// codemap. Returns (codepoints, true) on hit, (nil, false) on miss.
//
// Shortcodes that aren't in the kyokomi codemap (Slack-specific
// names, workspace customs) are not resolved here — call
// BuildCustomEmojiURL with the workspace customs map for those.
//
// VS16 and ZWJ are returned verbatim; URL construction strips VS16
// at BuildStandardEmojiURL time.
func CodepointsForShortcode(name string) ([]rune, bool) {
	key := ":" + name + ":"
	u, ok := emojilib.CodeMap()[key]
	if !ok {
		return nil, false
	}
	return []rune(u), true
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/emoji/ -run TestCodepointsForShortcode -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/url.go internal/emoji/url_test.go
git commit -m "feat(emoji): CodepointsForShortcode kyokomi lookup helper"
```

---

### Task 2.6: Failing test for `BuildCustomEmojiURL`

**Files:**
- Modify: `internal/emoji/url_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/emoji/url_test.go`:

```go
func TestBuildCustomEmojiURL(t *testing.T) {
	customs := map[string]string{
		"party_parrot": "https://emoji.slack-edge.com/T01/party_parrot/abc.gif",
		"company_logo": "https://emoji.slack-edge.com/T01/company_logo/def.png",
		"shipit":       "alias:rocket",                     // alias to a built-in
		"yay":          "alias:party_parrot",               // alias to a custom
		"chain_a":      "alias:chain_b",
		"chain_b":      "alias:chain_c",
		"chain_c":      "https://emoji.slack-edge.com/T01/chain_c/xyz.png",
		"loop_a":       "alias:loop_b",
		"loop_b":       "alias:loop_a",
	}

	cases := []struct {
		name    string
		wantURL string
		wantOK  bool
	}{
		// Direct custom: URL returned verbatim.
		{"party_parrot", "https://emoji.slack-edge.com/T01/party_parrot/abc.gif", true},
		{"company_logo", "https://emoji.slack-edge.com/T01/company_logo/def.png", true},

		// alias:<builtin>: resolves to the standard emoji URL.
		// rocket = U+1F680 = 1f680.png
		{"shipit", CDNBaseURL + "1f680.png", true},

		// alias:<custom>: resolves through to the custom's URL.
		{"yay", "https://emoji.slack-edge.com/T01/party_parrot/abc.gif", true},

		// Multi-hop alias chain.
		{"chain_a", "https://emoji.slack-edge.com/T01/chain_c/xyz.png", true},

		// Alias cycle: detected, returns ok=false.
		{"loop_a", "", false},

		// Unknown name: ok=false.
		{"never_defined", "", false},
	}
	for _, c := range cases {
		got, ok := BuildCustomEmojiURL(c.name, customs)
		if ok != c.wantOK || got != c.wantURL {
			t.Errorf("BuildCustomEmojiURL(%q) = (%q, %v), want (%q, %v)",
				c.name, got, ok, c.wantURL, c.wantOK)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestBuildCustomEmojiURL -v`
Expected: FAIL — `BuildCustomEmojiURL` is undefined.

---

### Task 2.7: Implement `BuildCustomEmojiURL`

**Files:**
- Modify: `internal/emoji/url.go`

- [ ] **Step 1: Add the helper**

Append to `internal/emoji/url.go`:

```go
// aliasPrefix marks a Slack custom-emoji alias entry (e.g.,
// "alias:thumbsup"). Mirrors the same constant in entries.go.
const customEmojiAliasPrefix = "alias:"

// maxAliasHops bounds alias chain resolution to defend against
// cyclic configurations (e.g., shadowed names that alias back to
// themselves through a different shortcode).
const maxAliasHops = 4

// BuildCustomEmojiURL resolves a shortcode name to a URL using the
// workspace's customs map first, then (if the customs map has an
// "alias:<target>" entry) the kyokomi codemap for the target.
//
// Returns (url, true) on hit. Returns ("", false) when:
//   - name is not in customs and is not a kyokomi builtin reachable
//     through an alias entry
//   - the alias chain cycles or exceeds maxAliasHops
//
// Direct kyokomi-known names (no customs entry) are intentionally
// NOT resolved here — call CodepointsForShortcode +
// BuildStandardEmojiURL for those. This separation keeps each
// function single-purpose and lets callers fall back to glyph
// rendering at the right granularity when one path fails.
func BuildCustomEmojiURL(name string, customs map[string]string) (string, bool) {
	visited := make(map[string]struct{}, maxAliasHops)
	current := name
	for hop := 0; hop < maxAliasHops; hop++ {
		if _, seen := visited[current]; seen {
			return "", false // cycle
		}
		visited[current] = struct{}{}

		entry, ok := customs[current]
		if !ok {
			return "", false
		}
		if !strings.HasPrefix(entry, customEmojiAliasPrefix) {
			// Direct custom URL.
			return entry, true
		}
		target := strings.TrimPrefix(entry, customEmojiAliasPrefix)

		// If the alias target is a kyokomi builtin, resolve it now.
		// If it's another custom name, loop and follow.
		if cps, ok := CodepointsForShortcode(target); ok {
			return BuildStandardEmojiURL(cps), true
		}
		current = target
	}
	return "", false // exceeded max hops
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/emoji/ -run TestBuildCustomEmojiURL -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/url.go internal/emoji/url_test.go
git commit -m "feat(emoji): BuildCustomEmojiURL with alias-chain resolution"
```

---

### Task 2.8: Convenience: `URLForShortcode`

**Files:**
- Modify: `internal/emoji/url.go`
- Modify: `internal/emoji/url_test.go`

**Why:** Callers in the token-resolution path need one function that "does the right thing" given a shortcode name: try the customs map, fall through to kyokomi, return ok=false if neither finds it. Without this helper every caller would repeat that precedence dance.

- [ ] **Step 1: Write the failing test**

Append to `internal/emoji/url_test.go`:

```go
func TestURLForShortcode(t *testing.T) {
	customs := map[string]string{
		"party_parrot": "https://emoji.slack-edge.com/T01/party_parrot/abc.gif",
		"thumbsup":     "https://emoji.slack-edge.com/T01/our_thumbs/def.png", // workspace override
	}
	cases := []struct {
		name    string
		wantURL string
		wantOK  bool
	}{
		// Workspace custom wins over kyokomi for the same name.
		{"thumbsup", "https://emoji.slack-edge.com/T01/our_thumbs/def.png", true},

		// Custom-only name.
		{"party_parrot", "https://emoji.slack-edge.com/T01/party_parrot/abc.gif", true},

		// kyokomi-only name.
		{"heart", CDNBaseURL + "2764.png", true},

		// Unknown.
		{"never_defined", "", false},
	}
	for _, c := range cases {
		got, ok := URLForShortcode(c.name, customs)
		if ok != c.wantOK || got != c.wantURL {
			t.Errorf("URLForShortcode(%q) = (%q, %v), want (%q, %v)",
				c.name, got, ok, c.wantURL, c.wantOK)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestURLForShortcode -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `internal/emoji/url.go`:

```go
// URLForShortcode resolves a Slack-style shortcode name (no colons)
// to a Slack CDN URL using the workspace customs map first
// (including alias resolution) and then the kyokomi codemap.
// Returns (url, true) on hit, ("", false) when both lookups miss
// or when an alias chain cycles.
//
// Workspace customs win over kyokomi for the same name — this
// matches the precedence already used in
// internal/emoji/entries.go:resolveCustomDisplay and the picker
// preview behavior.
func URLForShortcode(name string, customs map[string]string) (string, bool) {
	if u, ok := BuildCustomEmojiURL(name, customs); ok {
		return u, true
	}
	if cps, ok := CodepointsForShortcode(name); ok {
		return BuildStandardEmojiURL(cps), true
	}
	return "", false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/emoji/ -run TestURLForShortcode -v`
Expected: PASS.

- [ ] **Step 5: Run all emoji-package tests to confirm no regression**

Run: `go test ./internal/emoji/ -v`
Expected: all tests PASS, including the existing ones (`TestResolveShortcodesInText`, `TestShouldRenderUnicode`, etc.).

- [ ] **Step 6: Commit**

```bash
git add internal/emoji/url.go internal/emoji/url_test.go
git commit -m "feat(emoji): URLForShortcode precedence helper (customs > kyokomi)"
```

---

### Task 2.9: Final phase check

- [ ] **Step 1: Build the full project**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: no new failures.

Phase 2 complete. The URL library exists with full test coverage against captured Slack fixtures. No other code calls it yet — the feature remains dark. Continue to `03-token-model.md`.
