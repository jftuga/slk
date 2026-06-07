# More Themes + Channels-Panel Contrast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ~23 new built-in themes and give every theme's channels panel (sidebar) a perceptibly distinct surface from the message pane, guarded by a CIELAB ΔL* test.

**Architecture:** All themes remain entries in the `builtinThemes` map in `internal/ui/styles/themes.go`. We add a perceptual-contrast test (`TestChannelsPanelContrast`) using a test-local CIELAB `lstar(hex)` helper, retune the sidebar/rail backgrounds of existing dark themes to satisfy it, then add new theme entries in three batches (dark editor, light editor, Slack-branded). Docs counts are bumped last.

**Tech Stack:** Go, lipgloss v2, `github.com/pelletier/go-toml/v2`. Tests via `go test`. Lint via `golangci-lint`.

**Spec:** `docs/superpowers/specs/2026-06-07-more-themes-design.md`

**Working directory:** `.worktrees/more-themes` (branch `feat/more-themes`, already created off `origin/main`). Run all commands from there.

---

## Background the executor needs

- A theme is a `ThemeColors` struct (see `internal/ui/styles/themes.go:21-44`). Required fields: `Primary, Accent, Warning, Error, Background, Surface, SurfaceDark, Text, TextMuted, Border`. Optional: `SidebarBackground, SidebarText, SidebarTextMuted, RailBackground, SelectionBackground, SelectionForeground, ComposeInsertBG, SelectionBgFocused, SelectionBgUnfocused`.
- The map key is the **lowercase** display name (e.g. `"tokyo night"`); the struct's first field is the display `Name` (e.g. `"Tokyo Night"`). Lookups lowercase the name, so the key MUST equal `strings.ToLower(Name)`.
- The channels panel is painted with `SidebarBackground`; the message pane with `Background` (`internal/ui/styles/styles.go:387-398` and `403-406`). When `SidebarBackground` is empty it falls back to `Background` (`styles.go:305-309`) — i.e. **no contrast**.
- `RailBackground` (workspace rail) defaults to `SurfaceDark` when empty. Convention: rail is the darkest surface.
- Existing tests live in `internal/ui/styles/themes_test.go`. Do not break them.

### The contrast metric (used by Task 1's test)

CIELAB lightness `L*` derived from sRGB. For two backgrounds we require
`|L*(Background) − L*(SidebarBackground)| ≥ 6.0`. L* is perceptually uniform,
so one threshold works for light and dark themes. The difference is absolute:
near-black themes may use a **raised** (lighter) sidebar instead of a darker one.

Reference values (sanity checks): `slack default` white(#FFFFFF, L*≈100) vs
#434243 (L*≈28) → ΔL*≈72 (passes). Current `dark` #1A1A2E (L*≈10.2) vs
#13132B (L*≈7.0) → ΔL*≈3.2 (fails — this is what we fix).

---

## Task 1: CIELAB helper + contrast test + retune existing dark themes

**Files:**
- Modify: `internal/ui/styles/themes_test.go`
- Modify: `internal/ui/styles/themes.go`

- [ ] **Step 1.1: Add the CIELAB helper + contrast test**

Append to `internal/ui/styles/themes_test.go`. Add `"math"`, `"strconv"`, and `"strings"` to the import block if not already present (`strings` is already imported via the package; verify). The helper parses `#RRGGBB` directly (the test only runs on hex themes, so no lipgloss color-profile ambiguity):

```go
// --- channels-panel contrast guard ---

// contrastAllowlist are themes intentionally exempt from the
// channels-panel contrast requirement: the ANSI themes use palette
// numbers (not hex) and inherit the terminal, and "hot dog stand" is a
// deliberately garish novelty whose red-on-yellow split we don't want
// constraining the threshold.
var contrastAllowlist = map[string]bool{
	"ansi dark":     true,
	"ansi light":    true,
	"hot dog stand": true,
}

// minChannelsPanelDeltaLstar is the minimum perceptual lightness
// difference (CIELAB L*) between a theme's message-pane Background and
// its channels-panel SidebarBackground. 6.0 is calibrated so the
// slack-default split (ΔL*≈72) passes easily while a 1–2% nudge
// (ΔL*≈3) fails. See spec 2026-06-07-more-themes-design.md.
const minChannelsPanelDeltaLstar = 6.0

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// lstar returns the CIELAB L* (0..100) of a "#RRGGBB" hex string.
func lstar(hex string) float64 {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return 0
	}
	ri, _ := strconv.ParseInt(h[0:2], 16, 0)
	gi, _ := strconv.ParseInt(h[2:4], 16, 0)
	bi, _ := strconv.ParseInt(h[4:6], 16, 0)
	r := srgbToLinear(float64(ri) / 255)
	g := srgbToLinear(float64(gi) / 255)
	b := srgbToLinear(float64(bi) / 255)
	y := 0.2126*r + 0.7152*g + 0.0722*b
	if y > 0.008856 {
		return 116*math.Cbrt(y) - 16
	}
	return 903.3 * y
}

// TestChannelsPanelContrast asserts every non-allowlisted built-in
// theme gives the channels panel a perceptibly distinct surface from
// the message pane. Reports each theme's measured ΔL* on failure so the
// retune is a deterministic adjust-and-rerun loop.
func TestChannelsPanelContrast(t *testing.T) {
	for key, theme := range builtinThemes {
		if contrastAllowlist[key] {
			continue
		}
		bg := theme.Colors.Background
		sb := theme.Colors.SidebarBackground
		if sb == "" {
			sb = bg // falls back to Background -> zero contrast
		}
		if !strings.HasPrefix(bg, "#") || !strings.HasPrefix(sb, "#") {
			t.Errorf("theme %q: non-hex background/sidebar not allowlisted (bg=%q sidebar=%q)", key, bg, sb)
			continue
		}
		delta := math.Abs(lstar(bg) - lstar(sb))
		if delta < minChannelsPanelDeltaLstar {
			t.Errorf("theme %q channels-panel contrast too low: ΔL*=%.1f (bg=%s L*=%.1f, sidebar=%s L*=%.1f), want >= %.1f",
				key, delta, bg, lstar(bg), sb, lstar(sb), minChannelsPanelDeltaLstar)
		}
	}
}
```

- [ ] **Step 1.2: Run the contrast test — expect it to FAIL**

Run: `go test ./internal/ui/styles/ -run TestChannelsPanelContrast -v`
Expected: FAIL. The output lists existing dark themes whose ΔL* is below 6.0 (e.g. `dark`, `dracula`, `nord`, `tokyo night`, etc.), each with its measured ΔL*. Copy this list — it tells you exactly which themes Step 1.3 must fix and by how much.

- [ ] **Step 1.3: Retune existing dark themes**

In `internal/ui/styles/themes.go`, edit ONLY the `SidebarBackground` and `RailBackground` fields of the existing dark themes listed below. Do **not** touch `Background`, `Text`, accents, or any other field. Apply these starting values (chosen to clearly separate the sidebar; near-black themes use a *raised* sidebar):

| theme key | new SidebarBackground | new RailBackground |
|---|---|---|
| `dark` | `#0D0D1A` | `#060610` |
| `dracula` | `#14151C` | `#0C0C10` |
| `solarized dark` | `#001820` | `#000C10` |
| `gruvbox dark` | `#181818` | `#0E0E0E` |
| `nord` | `#1B2028` | `#11141A` |
| `tokyo night` | `#101119` | `#08090D` |
| `catppuccin mocha` | `#0E0E16` | `#08080F` |
| `one dark` | `#1A1D23` | `#101216` |
| `rosé pine` | `#100E18` | `#08070D` |
| `rosé pine moon` | `#15131F` | `#0C0B12` |
| `monokai` | `#181811` | `#0F0F0A` |
| `github dark` | `#1C2128` | `#0D1117` |
| `ayu mirage` | `#141822` | `#0B0E14` |
| `everforest dark` | `#1E2429` | `#141819` |
| `kanagawa` | `#131319` | `#0B0B0F` |
| `material ocean` | `#1A1C25` | `#090B10` |
| `synthwave` | `#150E20` | `#0B0714` |
| `catppuccin frappé` | `#1E212C` | `#14161D` |
| `catppuccin macchiato` | `#181A28` | `#0F1020` |
| `tokyo night storm` | `#181B29` | `#0F1019` |
| `cobalt2` | `#0C2030` | `#06141F` |
| `iceberg` | `#0D0E13` | `#070809` |
| `oceanic next` | `#122029` | `#0A1419` |
| `cyberpunk neon` | `#0D1B2A` | `#000814` |
| `material palenight` | `#1B1B28` | `#111119` |

Note on `github dark`, `material ocean`, `cyberpunk neon`: their `Background` is already near-black, so the table *raises* the sidebar (lighter than bg) and pushes the rail to the old background value. That is intentional.

- [ ] **Step 1.4: Run the contrast test — expect PASS, fix stragglers**

Run: `go test ./internal/ui/styles/ -run TestChannelsPanelContrast -v`
Expected: PASS. If any theme still fails, the error names it and prints its ΔL*. Remediation loop (deterministic):
- If the theme's `Background` L* is **> 12** (mid/light): make its `SidebarBackground` darker — subtract ~`0x10`–`0x18` from each RGB channel — and re-run.
- If the theme's `Background` L* is **<= 12** (near-black): make its `SidebarBackground` *lighter* (raise it toward its `Surface` value, add ~`0x10` per channel) and re-run.
- Keep `RailBackground` at or beyond the sidebar in the same direction (rail = darkest for normal themes; for raised-sidebar themes, rail = old background).
Repeat until PASS. Each iteration is mechanical — the test prints the exact gap.

- [ ] **Step 1.5: Run the whole styles package — expect PASS**

Run: `go test ./internal/ui/styles/ -v`
Expected: PASS (existing tests + new `TestChannelsPanelContrast`). `TestLightThemesHaveDarkSidebars` must still pass (we didn't touch light themes).

- [ ] **Step 1.6: Commit**

```bash
git add internal/ui/styles/themes.go internal/ui/styles/themes_test.go
git commit -m "feat(themes): add channels-panel contrast guard and retune dark sidebars

Add TestChannelsPanelContrast (CIELAB ΔL* >= 6.0) and deepen/raise the
SidebarBackground+RailBackground of existing dark themes so the channels
panel reads as a distinct surface from the message pane. Message-pane
backgrounds, text, and accents are unchanged."
```

---

## Task 2: Dark editor themes (batch 1)

**Files:**
- Modify: `internal/ui/styles/themes.go`
- Modify: `internal/ui/styles/themes_test.go`

- [ ] **Step 2.1: Write the failing registration + required-color tests**

Append to `internal/ui/styles/themes_test.go`:

```go
var darkEditorThemes = []string{
	"Zenburn", "Gruvbox Material Dark", "Nightfox", "Carbonfox",
	"Melange Dark", "Vesper", "Flexoki Dark", "Modus Vivendi",
	"Night Owl", "Poimandres", "Ayu Dark", "Kanagawa Dragon",
}

func TestDarkEditorThemesRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range ThemeNames() {
		have[n] = true
	}
	for _, want := range darkEditorThemes {
		if !have[want] {
			t.Errorf("dark editor theme %q not registered", want)
		}
	}
}

func TestDarkEditorThemesHaveRequiredColors(t *testing.T) {
	for _, name := range darkEditorThemes {
		c := lookupTheme(strings.ToLower(name))
		if c.Primary == "" || c.Accent == "" || c.Warning == "" || c.Error == "" ||
			c.Background == "" || c.Surface == "" || c.SurfaceDark == "" ||
			c.Text == "" || c.TextMuted == "" || c.Border == "" {
			t.Errorf("theme %q missing required color(s): %+v", name, c)
		}
	}
}
```

- [ ] **Step 2.2: Run — expect FAIL**

Run: `go test ./internal/ui/styles/ -run 'TestDarkEditorThemes' -v`
Expected: FAIL (themes not registered).

- [ ] **Step 2.3: Add the dark editor theme entries**

In `internal/ui/styles/themes.go`, add these entries to the `builtinThemes` map (anywhere before the closing `}`; grouping near related families is fine):

```go
	"zenburn": {"Zenburn", ThemeColors{
		Primary: "#8CD0D3", Accent: "#7F9F7F", Warning: "#F0DFAF", Error: "#CC9393",
		Background: "#3F3F3F", Surface: "#4F4F4F", SurfaceDark: "#2B2B2B",
		Text: "#DCDCCC", TextMuted: "#989890", Border: "#5F5F5F",
		SidebarBackground: "#2B2B2B", RailBackground: "#1F1F1F",
	}},
	"gruvbox material dark": {"Gruvbox Material Dark", ThemeColors{
		Primary: "#7DAEA3", Accent: "#A9B665", Warning: "#D8A657", Error: "#EA6962",
		Background: "#282828", Surface: "#32302F", SurfaceDark: "#1D2021",
		Text: "#D4BE98", TextMuted: "#928374", Border: "#45403D",
		SidebarBackground: "#1A1A1A", RailBackground: "#0F0F0F",
	}},
	"nightfox": {"Nightfox", ThemeColors{
		Primary: "#719CD6", Accent: "#81B29A", Warning: "#DBC074", Error: "#C94F6D",
		Background: "#192330", Surface: "#212E3F", SurfaceDark: "#131A24",
		Text: "#CDCECF", TextMuted: "#738091", Border: "#2B3B51",
		SidebarBackground: "#0E141C", RailBackground: "#080B11",
	}},
	"carbonfox": {"Carbonfox", ThemeColors{
		Primary: "#78A9FF", Accent: "#25BE6A", Warning: "#FF832B", Error: "#EE5396",
		Background: "#161616", Surface: "#262626", SurfaceDark: "#0C0C0C",
		Text: "#F2F4F8", TextMuted: "#7B7C7E", Border: "#393939",
		SidebarBackground: "#242424", RailBackground: "#0C0C0C",
	}},
	"melange dark": {"Melange Dark", ThemeColors{
		Primary: "#A3A9CE", Accent: "#85B695", Warning: "#EBC06D", Error: "#D47766",
		Background: "#292522", Surface: "#34302C", SurfaceDark: "#1F1B18",
		Text: "#ECE1D7", TextMuted: "#867462", Border: "#403A36",
		SidebarBackground: "#16130F", RailBackground: "#0D0B09",
	}},
	"vesper": {"Vesper", ThemeColors{
		Primary: "#FFC799", Accent: "#99FFE4", Warning: "#FFC799", Error: "#FF8080",
		Background: "#101010", Surface: "#1C1C1C", SurfaceDark: "#0A0A0A",
		Text: "#FFFFFF", TextMuted: "#8B8B8B", Border: "#2A2A2A",
		SidebarBackground: "#1F1F1F", RailBackground: "#000000",
	}},
	"flexoki dark": {"Flexoki Dark", ThemeColors{
		Primary: "#4385BE", Accent: "#879A39", Warning: "#D0A215", Error: "#D14D41",
		Background: "#100F0F", Surface: "#1C1B1A", SurfaceDark: "#0A0908",
		Text: "#CECDC3", TextMuted: "#878580", Border: "#282726",
		SidebarBackground: "#201F1E", RailBackground: "#0A0908",
	}},
	"modus vivendi": {"Modus Vivendi", ThemeColors{
		Primary: "#2FAFFF", Accent: "#44BC44", Warning: "#FEC43F", Error: "#FF5F59",
		Background: "#000000", Surface: "#1E1E1E", SurfaceDark: "#0A0A0A",
		Text: "#FFFFFF", TextMuted: "#989898", Border: "#303030",
		SidebarBackground: "#1A1A1A", RailBackground: "#0A0A0A",
	}},
	"night owl": {"Night Owl", ThemeColors{
		Primary: "#82AAFF", Accent: "#ADDB67", Warning: "#ECC48D", Error: "#EF5350",
		Background: "#011627", Surface: "#0E293F", SurfaceDark: "#010E1A",
		Text: "#D6DEEB", TextMuted: "#5F7E97", Border: "#1D3B53",
		SidebarBackground: "#0E293F", RailBackground: "#010E1A",
	}},
	"poimandres": {"Poimandres", ThemeColors{
		Primary: "#89DDFF", Accent: "#5DE4C7", Warning: "#FFFAC2", Error: "#D0679D",
		Background: "#1B1E28", Surface: "#252B37", SurfaceDark: "#171922",
		Text: "#E4F0FB", TextMuted: "#767C9D", Border: "#303340",
		SidebarBackground: "#0F1118", RailBackground: "#08090D",
	}},
	"ayu dark": {"Ayu Dark", ThemeColors{
		Primary: "#39BAE6", Accent: "#C2D94C", Warning: "#FFB454", Error: "#FF3333",
		Background: "#0B0E14", Surface: "#131721", SurfaceDark: "#06080D",
		Text: "#BFBDB6", TextMuted: "#565B66", Border: "#1B1F28",
		SidebarBackground: "#1B202B", RailBackground: "#0B0E14",
	}},
	"kanagawa dragon": {"Kanagawa Dragon", ThemeColors{
		Primary: "#8BA4B0", Accent: "#8A9A7B", Warning: "#C4B28A", Error: "#C4746E",
		Background: "#181616", Surface: "#282423", SurfaceDark: "#0D0C0C",
		Text: "#C5C9C5", TextMuted: "#737C73", Border: "#2D2C29",
		SidebarBackground: "#282423", RailBackground: "#100E0E",
	}},
```

- [ ] **Step 2.4: Run the styles package — expect PASS**

Run: `go test ./internal/ui/styles/ -run 'TestDarkEditorThemes|TestChannelsPanelContrast' -v`
Expected: PASS. `TestChannelsPanelContrast` now also validates these new themes. If any new theme fails contrast, apply the Step 1.4 remediation loop to that theme's `SidebarBackground`/`RailBackground`.

- [ ] **Step 2.5: Commit**

```bash
git add internal/ui/styles/themes.go internal/ui/styles/themes_test.go
git commit -m "feat(themes): add 12 dark editor themes with distinct channels panels"
```

---

## Task 3: Light editor themes (batch 2)

**Files:**
- Modify: `internal/ui/styles/themes.go`
- Modify: `internal/ui/styles/themes_test.go`

- [ ] **Step 3.1: Write the failing tests (registration, required colors, dark sidebars)**

Append to `internal/ui/styles/themes_test.go`:

```go
var lightEditorThemes = []string{
	"Rosé Pine Dawn", "Everforest Light", "Flexoki Light",
	"Modus Operandi", "Kanagawa Lotus", "PaperColor Light",
}

func TestLightEditorThemesRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range ThemeNames() {
		have[n] = true
	}
	for _, want := range lightEditorThemes {
		if !have[want] {
			t.Errorf("light editor theme %q not registered", want)
		}
	}
}

func TestLightEditorThemesHaveRequiredColors(t *testing.T) {
	for _, name := range lightEditorThemes {
		c := lookupTheme(strings.ToLower(name))
		if c.Primary == "" || c.Accent == "" || c.Warning == "" || c.Error == "" ||
			c.Background == "" || c.Surface == "" || c.SurfaceDark == "" ||
			c.Text == "" || c.TextMuted == "" || c.Border == "" {
			t.Errorf("theme %q missing required color(s): %+v", name, c)
		}
	}
}

func TestLightEditorThemesHaveDarkSidebars(t *testing.T) {
	for _, name := range lightEditorThemes {
		c := lookupTheme(strings.ToLower(name))
		if c.SidebarBackground == "" {
			t.Errorf("light theme %q must set SidebarBackground", name)
		}
		if c.RailBackground == "" {
			t.Errorf("light theme %q must set RailBackground", name)
		}
	}
}
```

- [ ] **Step 3.2: Run — expect FAIL**

Run: `go test ./internal/ui/styles/ -run 'TestLightEditorThemes' -v`
Expected: FAIL (not registered).

- [ ] **Step 3.3: Add the light editor theme entries**

In `internal/ui/styles/themes.go`, add to the `builtinThemes` map:

```go
	"rosé pine dawn": {"Rosé Pine Dawn", ThemeColors{
		Primary: "#56949F", Accent: "#286983", Warning: "#EA9D34", Error: "#B4637A",
		Background: "#FAF4ED", Surface: "#FFFAF3", SurfaceDark: "#F2E9E1",
		Text: "#575279", TextMuted: "#797593", Border: "#DFDAD9",
		SidebarBackground: "#575279", SidebarText: "#FAF4ED", SidebarTextMuted: "#9893A5",
		RailBackground: "#423F5C",
	}},
	"everforest light": {"Everforest Light", ThemeColors{
		Primary: "#3A94C5", Accent: "#8DA101", Warning: "#DFA000", Error: "#F85552",
		Background: "#FDF6E3", Surface: "#F4F0D9", SurfaceDark: "#EFEBD4",
		Text: "#5C6A72", TextMuted: "#939F91", Border: "#E0DCC7",
		SidebarBackground: "#343F44", SidebarText: "#D3C6AA", SidebarTextMuted: "#859289",
		RailBackground: "#232A2E",
	}},
	"flexoki light": {"Flexoki Light", ThemeColors{
		Primary: "#205EA6", Accent: "#66800B", Warning: "#AD8301", Error: "#AF3029",
		Background: "#FFFCF0", Surface: "#F2F0E5", SurfaceDark: "#E6E4D9",
		Text: "#100F0F", TextMuted: "#6F6E69", Border: "#DAD8CE",
		SidebarBackground: "#100F0F", SidebarText: "#CECDC3", SidebarTextMuted: "#878580",
		RailBackground: "#1C1B1A",
	}},
	"modus operandi": {"Modus Operandi", ThemeColors{
		Primary: "#0031A9", Accent: "#006800", Warning: "#6F5500", Error: "#A60000",
		Background: "#FFFFFF", Surface: "#F2F2F2", SurfaceDark: "#E5E5E5",
		Text: "#000000", TextMuted: "#595959", Border: "#D0D0D0",
		SidebarBackground: "#1E1E1E", SidebarText: "#FFFFFF", SidebarTextMuted: "#989898",
		RailBackground: "#000000",
	}},
	"kanagawa lotus": {"Kanagawa Lotus", ThemeColors{
		Primary: "#4D699B", Accent: "#6F894E", Warning: "#C4781E", Error: "#C84053",
		Background: "#F2ECBC", Surface: "#E7DBA0", SurfaceDark: "#DCD5AC",
		Text: "#545464", TextMuted: "#8A8980", Border: "#DCD5AC",
		SidebarBackground: "#1F1F28", SidebarText: "#DCD7BA", SidebarTextMuted: "#8A8980",
		RailBackground: "#16161D",
	}},
	"papercolor light": {"PaperColor Light", ThemeColors{
		Primary: "#0087AF", Accent: "#008700", Warning: "#D75F00", Error: "#AF0000",
		Background: "#EEEEEE", Surface: "#E4E4E4", SurfaceDark: "#D0D0D0",
		Text: "#444444", TextMuted: "#878787", Border: "#D0D0D0",
		SidebarBackground: "#1C1C1C", SidebarText: "#E4E4E4", SidebarTextMuted: "#878787",
		RailBackground: "#080808",
	}},
```

- [ ] **Step 3.4: Run the styles package — expect PASS**

Run: `go test ./internal/ui/styles/ -run 'TestLightEditorThemes|TestChannelsPanelContrast' -v`
Expected: PASS. Light themes have very high ΔL* (dark sidebar on light pane), so contrast passes comfortably.

- [ ] **Step 3.5: Commit**

```bash
git add internal/ui/styles/themes.go internal/ui/styles/themes_test.go
git commit -m "feat(themes): add 6 light editor themes (dark sidebars on light panes)"
```

---

## Task 4: Slack-branded themes (batch 3)

**Files:**
- Modify: `internal/ui/styles/themes.go`
- Modify: `internal/ui/styles/themes_test.go`

These recreate popular slackthemes.net looks. Each is paired with a tuned message pane. (Hoth and Monument were dropped: Hoth is a light sidebar on a light pane, which conflicts with the contrast goal; Monument lacks a reliable canonical palette.)

- [ ] **Step 4.1: Write the failing tests**

Append to `internal/ui/styles/themes_test.go`:

```go
var slackBrandedThemes = []string{
	"Aubergine", "Ochin", "Choco Mint", "Mocha", "Nocturne",
}

func TestSlackBrandedThemesRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range ThemeNames() {
		have[n] = true
	}
	for _, want := range slackBrandedThemes {
		if !have[want] {
			t.Errorf("slack-branded theme %q not registered", want)
		}
	}
}

func TestSlackBrandedThemesHaveRequiredColors(t *testing.T) {
	for _, name := range slackBrandedThemes {
		c := lookupTheme(strings.ToLower(name))
		if c.Primary == "" || c.Accent == "" || c.Warning == "" || c.Error == "" ||
			c.Background == "" || c.Surface == "" || c.SurfaceDark == "" ||
			c.Text == "" || c.TextMuted == "" || c.Border == "" {
			t.Errorf("theme %q missing required color(s): %+v", name, c)
		}
	}
}
```

- [ ] **Step 4.2: Run — expect FAIL**

Run: `go test ./internal/ui/styles/ -run 'TestSlackBrandedThemes' -v`
Expected: FAIL (not registered).

- [ ] **Step 4.3: Add the Slack-branded theme entries**

In `internal/ui/styles/themes.go`, add to the `builtinThemes` map:

```go
	"aubergine": {"Aubergine", ThemeColors{
		// Slack's classic dark-aubergine sidebar on a white message pane.
		Primary: "#1264A3", Accent: "#007A5A", Warning: "#ECB22E", Error: "#E01E5A",
		Background: "#FFFFFF", Surface: "#F8F8F8", SurfaceDark: "#F0F0F0",
		Text: "#1D1C1D", TextMuted: "#616061", Border: "#DDDDDD",
		SidebarBackground: "#4D394B", SidebarText: "#FFFFFF", SidebarTextMuted: "#BAA2B8",
		RailBackground: "#3E313C",
	}},
	"ochin": {"Ochin", ThemeColors{
		// Popular slate-blue Slack sidebar theme, light message pane.
		Primary: "#4A90D9", Accent: "#4CA64C", Warning: "#ECB22E", Error: "#EB4D5C",
		Background: "#FFFFFF", Surface: "#F6F7F8", SurfaceDark: "#EDEFF1",
		Text: "#1D1C1D", TextMuted: "#616061", Border: "#DCDFE3",
		SidebarBackground: "#303E4D", SidebarText: "#DAE3ED", SidebarTextMuted: "#8B97A5",
		RailBackground: "#232D38",
	}},
	"choco mint": {"Choco Mint", ThemeColors{
		// Dark-chocolate sidebar with a mint accent on a warm light pane.
		Primary: "#16A085", Accent: "#16C098", Warning: "#D9A441", Error: "#C0563B",
		Background: "#FAF7F2", Surface: "#F0EBE3", SurfaceDark: "#E6DFD5",
		Text: "#2B2017", TextMuted: "#6E6258", Border: "#DDD4C8",
		SidebarBackground: "#25190F", SidebarText: "#E8E2DB", SidebarTextMuted: "#A8998C",
		RailBackground: "#1A1109",
	}},
	"mocha": {"Mocha", ThemeColors{
		// Warm coffee sidebar on a light pane.
		Primary: "#A0522D", Accent: "#C58A5E", Warning: "#D89A4E", Error: "#B5453B",
		Background: "#F7F3F0", Surface: "#EDE7E2", SurfaceDark: "#E2DAD3",
		Text: "#2A2220", TextMuted: "#6B5E58", Border: "#DAD0C8",
		SidebarBackground: "#2E2422", SidebarText: "#E6DCD6", SidebarTextMuted: "#A38F86",
		RailBackground: "#211A18",
	}},
	"nocturne": {"Nocturne", ThemeColors{
		// Deep blue-black dark theme; sidebar raised for separation.
		Primary: "#4F9CD9", Accent: "#4FB477", Warning: "#E0B14F", Error: "#E0556B",
		Background: "#0F1620", Surface: "#18222F", SurfaceDark: "#0A0F16",
		Text: "#C3CCD9", TextMuted: "#66717F", Border: "#1F2C3A",
		SidebarBackground: "#1A2736", RailBackground: "#0A0F16",
	}},
```

- [ ] **Step 4.4: Run the styles package — expect PASS**

Run: `go test ./internal/ui/styles/ -run 'TestSlackBrandedThemes|TestChannelsPanelContrast' -v`
Expected: PASS. If `nocturne` (the only dark one) fails contrast, apply the Step 1.4 remediation loop (raise its `SidebarBackground`).

- [ ] **Step 4.5: Commit**

```bash
git add internal/ui/styles/themes.go internal/ui/styles/themes_test.go
git commit -m "feat(themes): add 5 slackthemes.net-inspired themes"
```

---

## Task 5: Docs, full suite, lint

**Files:**
- Modify: `README.md`
- Modify: `wiki/Features.md`
- Modify: `wiki/Configuration.md`

New theme total: 36 existing + 12 + 6 + 5 = **59**.

- [ ] **Step 5.1: Verify the count programmatically**

Run: `go test ./internal/ui/styles/ -run TestThemeCount -v` — this test does not exist yet; instead confirm the count by adding a quick assertion. Append to `internal/ui/styles/themes_test.go`:

```go
func TestBuiltinThemeCount(t *testing.T) {
	const want = 59
	if got := len(builtinThemes); got != want {
		t.Errorf("builtinThemes count = %d, want %d (update docs in README.md and wiki/Features.md if this changed intentionally)", got, want)
	}
}
```

Run: `go test ./internal/ui/styles/ -run TestBuiltinThemeCount -v`
Expected: PASS (count is 59). If it reports a different number, recount the entries you added (a dropped/duplicate theme), fix, and update `want` + the doc strings below to match the real number.

- [ ] **Step 5.2: Update README.md**

In `README.md`, replace the three "36" theme references with "59":
- Line ~4: `under 20MB` line — if it mentions a count, update it (it may not; only change theme-count mentions).
- Line ~17: `**Pretty.** 36 built-in themes,` → `**Pretty.** 59 built-in themes,`
- Line ~30: `- 36 themes + drop-in custom themes` → `- 59 themes + drop-in custom themes`

Use Grep to find exact occurrences first: search `README.md` for `36`. Only replace the ones referring to theme counts.

- [ ] **Step 5.3: Update wiki/Features.md**

Line ~95: `- 36 built-in themes (including ...` → `- 59 built-in themes (including ...` (keep the rest of the line unchanged).

- [ ] **Step 5.4: Add a contrast note to wiki/Configuration.md**

In `wiki/Configuration.md`, in the custom-themes section near the `sidebar_background` comment (around line 186-192), append a sentence:

```
Every built-in theme now sets a channels-panel (sidebar) background that is
perceptibly distinct from the message pane. When writing a custom theme,
set `sidebar_background` to a clearly darker (or, on near-black themes, a
slightly lighter) shade than `background` for the same effect.
```

- [ ] **Step 5.5: Full test suite + build + lint**

Run each and confirm green:
```bash
go build ./...
go test ./...
golangci-lint run
```
Expected: all PASS / no findings. If `golangci-lint` is not installed, run `make lint` (check the `Makefile` target) or note it for the reviewer.

- [ ] **Step 5.6: Smoke-test the binary launches**

Run: `go run ./cmd/slk --help` (or the appropriate entrypoint — check `cmd/`).
Expected: prints help/usage without panicking. (Full live theme preview is the reviewer's manual step via `Ctrl+y`.)

- [ ] **Step 5.7: Commit**

```bash
git add README.md wiki/Features.md wiki/Configuration.md internal/ui/styles/themes_test.go
git commit -m "docs(themes): bump theme count to 59 and document channels-panel contrast"
```

---

## Done criteria

- `go test ./...` green, including `TestChannelsPanelContrast` covering all 59 hex themes (minus the 3-theme allowlist).
- `golangci-lint run` clean.
- 23 new themes registered and selectable via `Ctrl+y`.
- Existing themes' message-pane backgrounds/text/accents unchanged; only sidebar/rail backgrounds retuned.
- Docs counts updated to 59.
- Reviewer previews themes live and signs off on palette quality.
