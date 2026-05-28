# Phase 4: Width Math & Probe Skip

> Index: `00-overview.md`. Previous: `03-token-model.md`. Next: `05-place-helper.md`.

> **Historical note (post-cleanup):** The startup width probe described in this phase has since been removed entirely. With image-emoji rendering as the default on kitty and the plan to deprecate non-kitty rendering paths, the probe became dead weight. `internal/emoji/{probe,cache,terminal,extras,init}.go` and their tests were deleted; `Width()` now falls back to `lipgloss.Width()` when image mode is inactive. The image-mode branch documented below remains the active code path. See the `refactor(emoji): remove startup width-probe machinery` commit.

**Goal:** When emoji-image mode is active (kitty + `EmojiImages=on`), make `emojiutil.Width()` report a fixed 2 cells (configurable via `EmojiCells`) for every emoji-renderable grapheme cluster, bypassing the probe map. Also skip the startup emoji width probe entirely in that mode, saving ~200ms-30s of user-visible startup cost.

The mode is a single process-global toggle set once at startup before bubbletea starts. Width math consults the toggle on every measurement.

**Files:**
- Create: `internal/emoji/imagemode.go`
- Create: `internal/emoji/imagemode_test.go`
- Modify: `internal/emoji/width.go` (image-mode branch in `Width()`)
- Modify: `internal/emoji/width_test.go` (image-mode coverage)
- Modify: `cmd/slk/main.go` (gate the probe + set the mode)

---

### Task 4.1: Image-mode setter skeleton

**Files:**
- Create: `internal/emoji/imagemode.go`

- [ ] **Step 1: Write the skeleton**

Create `internal/emoji/imagemode.go`:

```go
package emoji

import "sync"

// Image-mode is a process-global flag that records whether emoji
// should be rendered as PNG images via the kitty graphics protocol.
// When active, the width math, render, and shouldn't-probe decisions
// all branch off this single value.
//
// Set once at startup (cmd/slk/main.go) after the user's config has
// been loaded and the terminal's image protocol has been detected.
// Not safe to toggle dynamically — UI surfaces snapshot the value
// at View() time but the kitty image upload pipeline holds session
// state that isn't designed for mid-session protocol changes.
var (
	imageModeMu     sync.RWMutex
	imageModeActive bool
	imageModeCells  = 2
)

// SetImageMode records whether emoji should be rendered as images.
// cells must be 1 or 2; other values clamp to 2. Safe to call at
// most once during process startup, before bubbletea begins.
func SetImageMode(active bool, cells int) {
	if cells != 1 && cells != 2 {
		cells = 2
	}
	imageModeMu.Lock()
	defer imageModeMu.Unlock()
	imageModeActive = active
	imageModeCells = cells
}

// ImageModeActive reports whether emoji-as-images is enabled.
// Cheap RLock under the hood; safe to call from any goroutine.
func ImageModeActive() bool {
	imageModeMu.RLock()
	defer imageModeMu.RUnlock()
	return imageModeActive
}

// ImageModeCells returns the per-emoji cell footprint (typically 2).
// Returns 2 when image mode is inactive; callers should gate on
// ImageModeActive before using this value.
func ImageModeCells() int {
	imageModeMu.RLock()
	defer imageModeMu.RUnlock()
	return imageModeCells
}

// resetImageMode clears the mode. Test-only helper.
func resetImageMode() {
	imageModeMu.Lock()
	defer imageModeMu.Unlock()
	imageModeActive = false
	imageModeCells = 2
}
```

- [ ] **Step 2: Build to confirm**

Run: `go build ./internal/emoji/`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/imagemode.go
git commit -m "feat(emoji): SetImageMode/ImageModeActive process-global toggle"
```

---

### Task 4.2: Failing test for `SetImageMode`

**Files:**
- Create: `internal/emoji/imagemode_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/emoji/imagemode_test.go`:

```go
package emoji

import "testing"

func TestImageMode_DefaultsToInactive(t *testing.T) {
	resetImageMode()
	if ImageModeActive() {
		t.Errorf("ImageModeActive() should be false by default")
	}
	if ImageModeCells() != 2 {
		t.Errorf("ImageModeCells() default = %d, want 2", ImageModeCells())
	}
}

func TestImageMode_SetActivates(t *testing.T) {
	resetImageMode()
	SetImageMode(true, 2)
	if !ImageModeActive() {
		t.Errorf("after SetImageMode(true, 2): ImageModeActive() = false, want true")
	}
	if ImageModeCells() != 2 {
		t.Errorf("ImageModeCells() = %d, want 2", ImageModeCells())
	}
}

func TestImageMode_ClampsCells(t *testing.T) {
	resetImageMode()
	for _, c := range []int{0, -1, 3, 99} {
		SetImageMode(true, c)
		if ImageModeCells() != 2 {
			t.Errorf("SetImageMode(true, %d): cells = %d, want clamp to 2", c, ImageModeCells())
		}
	}
	SetImageMode(true, 1)
	if ImageModeCells() != 1 {
		t.Errorf("SetImageMode(true, 1): cells = %d, want 1", ImageModeCells())
	}
}

func TestImageMode_DeactivateRoundTrip(t *testing.T) {
	resetImageMode()
	SetImageMode(true, 2)
	SetImageMode(false, 2)
	if ImageModeActive() {
		t.Errorf("after SetImageMode(false, _): ImageModeActive() = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/emoji/ -run TestImageMode -v`
Expected: all PASS (the skeleton from Task 4.1 already satisfies these).

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/imagemode_test.go
git commit -m "test(emoji): cover SetImageMode/ImageModeActive contract"
```

---

### Task 4.3: Failing test — Width with image mode active

**Files:**
- Modify: `internal/emoji/width_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/emoji/width_test.go`:

```go
func TestWidth_ImageMode_KnownEmojiClusters(t *testing.T) {
	resetImageMode()
	resetWidthMap()
	t.Cleanup(func() {
		resetImageMode()
		resetWidthMap()
	})

	SetImageMode(true, 2)

	cases := []struct {
		name string
		in   string
		want int
	}{
		// Each single emoji reports the image-mode footprint (2 cells).
		{"single thumb", "\U0001F44D", 2},
		{"VS16 sequence", "\u2764\uFE0F", 2},
		{"ZWJ sequence", "\U0001F468\u200D\U0001F680", 2},
		{"regional indicator pair", "\U0001F1FA\U0001F1F8", 2},

		// Mixed text + emoji: emoji = 2, plus the ASCII run.
		{"text + emoji", "hi \U0001F44D", 5}, // "hi " (3) + emoji (2)
		{"emoji + text", "\U0001F44D hi", 5}, // emoji (2) + " hi" (3)

		// Two adjacent emoji.
		{"emoji + emoji", "\U0001F44D\u2764\uFE0F", 4},

		// Pure ASCII: probe-map / lipgloss path unchanged.
		{"ascii only", "hello", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Width(c.in)
			if got != c.want {
				t.Errorf("Width(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestWidth_ImageMode_OneCellOverride(t *testing.T) {
	resetImageMode()
	resetWidthMap()
	t.Cleanup(func() {
		resetImageMode()
		resetWidthMap()
	})

	SetImageMode(true, 1)
	if got := Width("\U0001F44D"); got != 1 {
		t.Errorf("Width(thumb) with cells=1 = %d, want 1", got)
	}
	if got := Width("hi \U0001F44D"); got != 4 {
		t.Errorf("Width('hi ' + thumb) with cells=1 = %d, want 4 ('hi ' + 1)", got)
	}
}

func TestWidth_ImageMode_InactiveFallsThrough(t *testing.T) {
	resetImageMode()
	resetWidthMap()
	t.Cleanup(func() {
		resetImageMode()
		resetWidthMap()
	})

	// Mode is inactive — should NOT force 2-cell width for emoji
	// clusters; behavior comes from probe map (empty here) or lipgloss
	// fallback. Confirm by checking ASCII unaffected and emoji uses the
	// non-image-mode path (which may be != 2, depending on lipgloss).
	if got := Width("hello"); got != 5 {
		t.Errorf("Width(ascii) with image-mode off = %d, want 5", got)
	}
	// The exact width for an emoji here is whatever lipgloss reports —
	// we only assert that ImageModeActive is false so the new branch
	// is not taken.
	if ImageModeActive() {
		t.Fatalf("ImageModeActive() = true after resetImageMode()")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestWidth_ImageMode -v`
Expected: the first two test functions FAIL (`Width` doesn't yet consult ImageMode); the third passes because it only asserts the mode-off path which already works.

---

### Task 4.4: Implement the image-mode branch in `Width()`

**Files:**
- Modify: `internal/emoji/width.go`

- [ ] **Step 1: Update `Width()` to branch on image mode**

Replace the body of `Width()` in `internal/emoji/width.go` (around line 26) with:

```go
func Width(s string) int {
	// Strip ANSI escape sequences first. Without this, uniseg would
	// segment ESC bytes and parameter bytes as individual graphemes,
	// each measuring as width 1 — wildly inflating the result for any
	// styled string from lipgloss.
	stripped := ansi.Strip(s)

	if !containsNonASCII(stripped) {
		return lipgloss.Width(stripped)
	}

	// Image-mode fast path: for every grapheme cluster known to render
	// as an emoji image, return the configured cell footprint (typically
	// 2) instead of consulting the probe map or lipgloss. This bypass
	// is what retires the per-terminal alignment-drift bug — we report
	// a width we know the kitty renderer will deliver exactly.
	imageActive := ImageModeActive()
	imageCells := 0
	if imageActive {
		imageCells = ImageModeCells()
	}

	widthMu.RLock()
	cached := widthMap
	widthMu.RUnlock()

	if !imageActive && len(cached) == 0 {
		return lipgloss.Width(stripped)
	}

	total := 0
	gr := uniseg.NewGraphemes(stripped)
	for gr.Next() {
		cluster := gr.Str()
		if imageActive && isKnownEmojiCluster(cluster) {
			total += imageCells
			continue
		}
		if w, ok := cached[cluster]; ok {
			total += w
		} else {
			total += lipgloss.Width(cluster)
		}
	}
	return total
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/emoji/ -run TestWidth -v`
Expected: PASS — both new `TestWidth_ImageMode_*` and existing `TestWidth*` tests.

If the existing tests fail (e.g., one that exercised the old early-return-when-cache-empty path), the change above re-routed that case through the cluster loop. The behavior is equivalent (cluster loop falls back to `lipgloss.Width(cluster)` per cluster), but if a test asserts exact lipgloss call counts, adjust it to match the new code path.

- [ ] **Step 3: Commit**

```bash
git add internal/emoji/width.go internal/emoji/width_test.go
git commit -m "feat(emoji): force emoji-cluster width to ImageModeCells when active"
```

---

### Task 4.5: Failing test — probe is skipped when image-mode active

**Files:**
- Modify: `internal/emoji/init_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/emoji/init_test.go`:

```go
func TestWillProbe_RespectsImageMode(t *testing.T) {
	codemap := map[string]string{":a:": "A", ":b:": "B"}

	// Image-mode active: probe should be skipped regardless of cache
	// presence (no cache file exists in this TempDir).
	resetImageMode()
	SetImageMode(true, 2)
	t.Cleanup(func() { resetImageMode() })

	if WillProbe(InitOptions{Codemap: codemap}) {
		t.Errorf("WillProbe() with image mode active = true, want false")
	}

	// Image-mode off: WillProbe should return true on an uncached system.
	resetImageMode()
	if !WillProbe(InitOptions{Codemap: codemap}) {
		t.Errorf("WillProbe() with image mode inactive (no cache) = false, want true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/emoji/ -run TestWillProbe_RespectsImageMode -v`
Expected: the first assertion FAILS — `WillProbe` doesn't yet consult image mode.

---

### Task 4.6: Make `WillProbe` and `Init` honor image mode

**Files:**
- Modify: `internal/emoji/init.go`

- [ ] **Step 1: Short-circuit `WillProbe` when image mode is active**

In `internal/emoji/init.go`, modify `WillProbe` (around line 41):

```go
func WillProbe(opts InitOptions) bool {
	if opts.SkipProbe {
		return false
	}
	if ImageModeActive() {
		return false
	}
	if opts.ForceProbe {
		return true
	}
	// ... rest of function unchanged
```

- [ ] **Step 2: Short-circuit `initWithIO` when image mode is active**

In the same file, modify `initWithIO` (around line 94) to add an early return after the option-default block:

```go
func initWithIO(opts InitOptions, out io.Writer, in io.Reader) (bool, bool, error) {
	if opts.Codemap == nil {
		opts.Codemap = emojilib.CodeMap()
	}
	if opts.PerProbeTimeout == 0 {
		opts.PerProbeTimeout = 200 * time.Millisecond
	}

	// Image-mode active: width measurement is bypassed for known emoji
	// clusters (see Width() in width.go). The probe data is unused, so
	// skip the probe entirely — saves ~30s of user-visible startup on
	// first launch.
	if ImageModeActive() {
		debuglog.ImgRender("emoji probe skipped: image mode active")
		return false, false, nil
	}

	// ... rest of function unchanged (terminalKey, cache load, probe, etc.)
```

Add the `debuglog` import if not already present:

```go
import (
	// ... existing imports
	"github.com/gammons/slk/internal/debuglog"
)
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./internal/emoji/ -run TestWillProbe -v`
Expected: PASS for both `TestWillProbe_RespectsImageMode` and the existing `TestWillProbe_*` tests.

- [ ] **Step 4: Run the full emoji-package test suite**

Run: `go test ./internal/emoji/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/emoji/init.go internal/emoji/init_test.go
git commit -m "feat(emoji): skip width probe when image mode is active"
```

---

### Task 4.7: Wire image-mode into `cmd/slk/main.go`

**Files:**
- Modify: `cmd/slk/main.go`

**Why:** The current startup order is `emoji-probe-init → run() → image-protocol-detect`. For image mode to skip the probe, we need to know the protocol *before* the probe runs. Solution: pre-detect the protocol (env-based, non-interactive — cheap) at startup, decide image mode, then run the probe init (which now no-ops when image mode is active).

The kitty version probe at line 611 (inside `run()`) still runs and may downgrade kitty→halfblock. In that rare case we've already skipped the emoji probe; the user gets lipgloss-fallback widths for non-trivial clusters. Acceptable tradeoff for the common-case startup win, and documented.

- [ ] **Step 1: Read the current pre-probe block to anchor the change**

The probe init is at `cmd/slk/main.go:395-431`. The Detect call is at `cmd/slk/main.go:606` (inside `run()`). We're inserting between them.

- [ ] **Step 2: Insert protocol pre-detect and image-mode wiring**

In `cmd/slk/main.go`, locate the existing probe-init block (around line 395-431, the `skipProbe := false ... emojiwidth.Init(probeOpts)` flow). The current code uses `emojiwidth` (the import alias for `internal/emoji`). Add image-mode wiring at the top of `main()` once `cfg` is available (after `cfg, err := config.Load(...)` returns — search for `config.Load(` to find the exact line) but before the existing probe block.

The skeleton change (insert immediately above the existing `skipProbe := false` line):

```go
	// Pre-detect the image rendering protocol (env-based; non-interactive)
	// so we can decide whether to skip the emoji width probe entirely.
	// Image mode is active when the user has requested it AND we have
	// reasonable confidence kitty will be the final protocol. The
	// interactive kitty version probe in run() may still downgrade
	// kitty→halfblock; in that rare case the user has already paid the
	// probe-skip and will see lipgloss-fallback widths for non-trivial
	// clusters. Acceptable tradeoff for the common-case startup win.
	preDetectedProto := imgpkg.Detect(imgpkg.CaptureEnv(), cfg.Appearance.ImageProtocol)
	imageMode := cfg.Appearance.EmojiImages == "on" && preDetectedProto == imgpkg.ProtoKitty
	emojiwidth.SetImageMode(imageMode, cfg.Appearance.EmojiCells)
	if imageMode {
		debuglog.ImgRender("emoji image mode: ON (pre-detected proto=%s, emoji_images=%q)",
			preDetectedProto, cfg.Appearance.EmojiImages)
	} else {
		debuglog.ImgRender("emoji image mode: OFF (pre-detected proto=%s, emoji_images=%q)",
			preDetectedProto, cfg.Appearance.EmojiImages)
	}

	skipProbe := false
	// ... existing code continues unchanged
```

Add the `imgpkg` import if not already present in this part of main.go (it is — used at line 597+; confirm by grep). Same for `debuglog`.

- [ ] **Step 3: Build to verify**

Run: `go build ./...`
Expected: clean. Compile errors here would indicate a missing import or symbol name — `emojiwidth` is the existing alias for `internal/emoji` (confirm via `grep emojiwidth cmd/slk/main.go | head -5`).

- [ ] **Step 4: Smoke-run with image mode active**

Run: `go run ./cmd/slk --help 2>&1 | head -1`
Expected: clean exit. (The `--help` path doesn't hit bubbletea; this just confirms the new block doesn't panic at startup.)

Optionally with debug logging: `SLK_DEBUG=1 go run ./cmd/slk --help 2>&1 | grep "image mode"`. Expected: one line confirming the chosen mode.

- [ ] **Step 5: Commit**

```bash
git add cmd/slk/main.go
git commit -m "feat(slk): wire emoji image mode into startup; skip probe on kitty"
```

---

### Task 4.8: Final phase check

- [ ] **Step 1: Build the full project**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: no new failures.

- [ ] **Step 3: Confirm width math integrates with the rest of the package**

Run: `go test ./internal/emoji/ -v`
Expected: all tests PASS — `TestWidth`, `TestWidth_ImageMode_*`, `TestImageMode_*`, `TestWillProbe_RespectsImageMode`, plus all pre-existing tests (`TestResolveShortcodesInText`, `TestBuild*`, `TestResolveEmojiToTokens_*`, etc.).

Phase 4 complete. Width math and the startup probe now honor image mode. The feature is still dark — no UI surface has been switched to consume tokens yet — but the foundation library is fully wired and all decisions about width and probing are correct. Continue to `05-place-helper.md`.
