# Emoji as Images Design

## Motivation

slk's emoji rendering has accumulated a problem-shaped pile of code. The terminal probe in `internal/emoji/` measures every emoji's actual rendered width at startup and caches the result. The `ShouldRenderUnicode` rule in `internal/emoji/shouldrender.go:1` deliberately falls back to `:name:` text for any multi-codepoint sequence (ZWJ, flags, skin tones, VS16) because lipgloss reports one width and the terminal+font renders another, breaking border alignment. Slack workspace custom emoji never render at all — they show as `:party_parrot:` text in every surface, even though Slack's API gives us their image URLs in `internal/slack/client.go:629`.

Meanwhile, slk already has a complete image rendering pipeline for inline message attachments and avatars: the kitty graphics protocol with unicode placeholders (`internal/image/kitty.go`), disk-backed LRU cache (`internal/image/cache.go`), singleflight-deduplicated HTTP fetcher (`internal/image/fetcher.go`), stable per-(key, target) image-ID registry (`internal/image/registry.go`), tmux DCS passthrough wrapping, and prerender-off-the-UI-thread machinery.

This design replaces glyph emoji rendering with image emoji rendering on kitty-class terminals, reusing that infrastructure. Slack web already serves all emoji — both standard and workspace-custom — as PNGs from `a.slack-edge.com`. Pointing the existing fetcher at those URLs solves three problems with one mechanism: multi-codepoint emoji render correctly, custom workspace emoji finally appear as their actual artwork, and per-terminal width disagreement vanishes for emoji because we declare the cell footprint and the terminal renders pixels into exactly that footprint.

## Scope

| Surface | Image rendering in v1 |
|---|---|
| Message body text (main pane) | yes |
| Reaction pills | yes |
| Thread pane (mirrors main pane) | yes |
| Reaction picker grid | yes |
| Emoji autocomplete dropdown | yes |
| Compose input (text being typed) | **no — stays raw `:name:` text** |
| Channel list emoji prefixes | deferred |
| User status / display-name emoji | deferred |

Compose input is intentionally excluded: inline images in an editable text field break cursor positioning, selection, backspace-by-grapheme, and the user's mental model. The same separation is used by Slack web, Discord, and iMessage — the *rendered* form is image, the *editing* form is text.

Non-kitty terminals (sixel, halfblock, `image_protocol = off`) keep the current glyph + probe + `:name:`-fallback pipeline unchanged. The intent is to eventually delete those paths, but that is out of scope here.

## Architecture

**Asset source: Slack CDN only.** All emoji PNGs come from `a.slack-edge.com`. No bundled Twemoji or Openmoji assets, no per-workspace emoji-style detection in v1 — the URL prefix is a single constant `https://a.slack-edge.com/production-standard-emoji-assets/16.0/google-small/` that we can change in one place. Workspace customs use the URL Slack returns from `users.list` / `emoji.list`.

**Cell footprint: 2 cells wide × 1 row tall, fixed.** This is the central decision that retires the alignment bug. With kitty's unicode-placeholder mode we declare how many cells an image occupies; the terminal renders pixels into exactly that footprint, and no font, no Unicode property table, and no width-probe can disagree. 2 cells matches `East_Asian_Width=W` (which is what the Unicode standard says most emoji already are) and produces a roughly square pixel region in typical ~2:1 cell aspect ratios. Configurable via `[appearance] emoji_cells = 2 | 1` as an escape hatch if visual testing finds 2 too large.

**Pipeline:** Today `ResolveShortcodesInText` returns a flat string with `:name:` replaced by unicode glyphs (or kept as text when `ShouldRenderUnicode` rejects). The new model returns a token stream:

```
[TextRun("Great work! "),
 EmojiToken{url: "...1f44d.png", cells: 2, plainText: "👍"},
 TextRun(" "),
 EmojiToken{url: customs["party_parrot"], cells: 2, plainText: ":party_parrot:"},
 ...]
```

Each `EmojiToken` carries both its image URL and its plain-text representation. Rendering emits the kitty placeholder string; yank, copy, and search use `plainText`.

**Two kinds of emoji are detected in source text:**

1. `:shortcode:` matches (existing `shortcodeRe` regex). Resolved against the workspace customs map first (handles alias chains), then kyokomi's codemap (built-in name → codepoint sequence → URL).
2. Unicode grapheme clusters carrying the `Extended_Pictographic` property. Detected via `uniseg` cluster iteration; codepoint sequence converted to URL.

**URL construction.** Standard emoji: lowercased hex codepoints, dash-joined; ALL codepoints are preserved (including U+FE0F / VS16). Earlier drafts of this design hypothesized VS16 was stripped to match Slack web's display URLs; empirical fetches against the CDN (`2764.png` → 403, `2764-fe0f.png` → 200) corrected that. Custom emoji: use the URL Slack returns directly; resolve `alias:target` chains with a hop limit. The actual URL shape is locked in by a fixture of real Slack URLs (`❤️`, `⚠️`, `🏳️‍🌈`, `👨‍🚀`, `👍🏽`, `🇺🇸`).

**Per-emoji rendering** follows the same path as inline image attachments today:
1. Token's URL → `Fetcher.Cached(url, target = 2×1cells)`.
2. **Warm path** (already transmitted this session): returns the 2-character kitty unicode-placeholder string plus a no-op flush.
3. **Cold path:** kicks off async fetch, returns a 2-cell space reservation. Width math still reports 2 (no layout shift on swap). When the fetch completes, the existing `ImageReadyMsg` triggers a re-render and the reservation becomes pixels.
4. **404 or decode error:** negative-cache the URL; for that single emoji, fall back to the current glyph rendering path. Other emoji unaffected.

**Image dedup is free.** `internal/image/registry.go` mints stable image IDs per `(cache_key, target_cells)`. URL-as-cache-key + fixed 2×1 footprint means every instance of `:thumbsup:` across the message pane, thread pane, picker, and autocomplete shares one kitty image ID. One transmission per unique emoji per session, infinite cheap placements thereafter.

**Width math** routes through a single function (`emojiutil.Width`, verified to be the universal measurement point for wrap math, hit-testing, pill packing, and layout). On kitty + `emoji_images=on`:
- Cluster is an image-renderable emoji → width = `emoji_cells` (default 2), bypass the probe map.
- Cluster is anything else → existing probe / lipgloss path.

This is the change that makes the alignment bug structurally impossible for emoji.

**Startup probe** is skipped entirely on kitty + `emoji_images=on`. Probe still runs on non-kitty terminals and when `emoji_images=off` is forced. Net: ~200ms faster startup for the common case.

## Files Added and Changed

| File | Change |
|---|---|
| `internal/emoji/url.go` *(new)* | `BuildStandardEmojiURL([]rune) string` and `BuildCustomEmojiURL(name, customs) string`. Single constant for the CDN prefix. |
| `internal/emoji/tokens.go` *(new)* | `ResolveEmojiToTokens(text, customs) []Token`. Emoji-cluster detection via uniseg. |
| `internal/emoji/width.go` | When image path active for the current terminal, image-renderable clusters report `emoji_cells`; everything else unchanged. |
| `internal/emoji/init.go` | Skip probe on kitty + `emoji_images=on`. |
| `internal/emoji/shouldrender.go` | Untouched. Still used on the non-kitty fallback path. |
| `internal/image/fetcher.go` | No API change. Verify the "tiny target + high repetition" workload is efficient. |
| `internal/ui/messages/model.go`, `internal/ui/thread/model.go` | Replace flat-string resolution with token-stream rendering. |
| `internal/ui/reactionpicker/model.go` | Render pill emoji as image tokens. |
| Compose autocomplete dropdown | Render dropdown rows as image tokens (next to the searchable name text). |
| `internal/config/config.go` | Add `Appearance.EmojiImages string` (default `"on"`) and `Appearance.EmojiCells int` (default `2`). |
| `internal/ui/app.go` | Gate on detected protocol = kitty + `emoji_images = "on"`. |

## Caching

Reuse `internal/image/cache.go` as-is with URL as the cache key. Emoji PNGs are ~2KB each; the default `max_image_cache_mb = 200` (`config.go:152`) fits ~100,000 emoji — well beyond any workspace. Avatars, attachments, and emoji share the same LRU budget. `Cache.Get` refreshes access time on every read, so frequently-used emoji are never evicted by a burst of large attachments.

What persists across restart: raw PNG bytes on disk. What rebuilds on session start: decoded `image.Image` (in-memory `sync.Map`), pre-rendered kitty payload (in-memory `sync.Map`), and the kitty image-ID registry. That means a brief blank-then-fill cycle on the first encounter with each emoji in a new session — same UX as today's avatars and attachments, deemed acceptable for that pattern.

Optimizations deferred to post-implementation:
- Persisted decoded-pixel cache (skip PNG decode on session start).
- Persisted kitty payload + stable cross-session image IDs (skip re-transmit).

## Failure Modes

| Failure | Behavior |
|---|---|
| CDN unreachable / DNS blocked | Cold-cache cells stay reserved-blank; warm-cache emoji render fine. Existing fetcher retry/backoff applies. `emoji_images = "off"` forces glyph fallback. |
| URL 404 | Negative-cache with TTL ~24h. That emoji falls back to glyph rendering. Logged once at `[imgfetch]`. |
| Custom emoji URL 403 / stale | Same as 404. Refresh via existing `CustomEmojisLoadedMsg` re-fetches normally. |
| Corrupt PNG decode | Same as 404. |
| Image fetch slow on cold cache | Reserved space stays blank until `ImageReadyMsg`, then re-renders. |
| Disk cache eviction (LRU pressure) | Re-fetches on next encounter. Emoji are small; eviction pressure from emoji alone is negligible. |
| Tmux passthrough | Existing tmux detection + DCS wrapping in the kitty path handles this. |
| `emoji_images = "on"` on non-kitty terminal | Silently treated as `"off"`. Logged at startup. |
| Custom emoji alias cycle | Max-depth limit (3 hops). On cycle, fall back to glyph for the original shortcode. |
| Workspace custom shadows a builtin name | Custom wins. Existing precedence preserved. |

## Integration Points

- **Image fetcher reuse.** Emoji are another caller of `internal/image/fetcher.go` with `CellTarget = (2, 1)`. No new fetch subsystem.
- **Kitty registry.** `(URL, 2×1)` mints a stable image ID — same emoji across all surfaces shares one ID, one transmission per session.
- **lipgloss + kitty placeholders.** The inline-attachment path already proves styled text can wrap a kitty placeholder rune without corrupting the SGR image-ID encoding. Reaction pill borders, mention highlights, and code-adjacent emoji rely on the same pattern.
- **Re-render on customs load.** `CustomEmojisLoadedMsg` already triggers a re-render. With this feature, that same re-render kicks off fetches for newly-known URLs.
- **Yank / copy-to-clipboard** must emit `EmojiToken.plainText`, never the kitty placeholder runes. This is a behavior to verify explicitly during implementation, not assume.
- **Search / find-in-buffer** must match against `plainText`. Same plumbing as yank.
- **Compose input** is unaffected. Raw `:name:` text in, raw `:name:` text on screen, raw `:name:` text out the wire to Slack. The resolve-to-tokens path runs only on the rendering side.

## Configuration

```toml
[appearance]
emoji_images = "on"   # "on" | "off"; default "on". Non-kitty terminals treat "on" as "off".
emoji_cells  = 2      # 2 | 1; default 2.
```

Documented in `wiki/Configuration.md` and `wiki/Terminal-Compatibility.md` (the latter as the escape hatch for CDN-blocked networks).

## Testing

| Layer | Test type | Proves |
|---|---|---|
| `internal/emoji/url.go` | Table-driven unit tests | Codepoints → URL: single, ZWJ, flags, skin tones, VS16 preserved. Fixture seeded from live Slack URLs. |
| `internal/emoji/url.go` | Unit tests | Custom emoji: direct, alias hop, alias chain, alias cycle (returns fallback), unknown name. |
| `internal/emoji/tokens.go` | Unit tests | `ResolveEmojiToTokens` on ASCII-only, all-emoji, mixed, adjacent emoji, start/end positions, broken shortcodes. |
| `internal/emoji/width.go` | Unit tests | Image-path active: emoji clusters report `emoji_cells`; non-emoji clusters route through probe. Image-path inactive: behavior unchanged. |
| `internal/emoji/init.go` | Unit tests | Probe skipped on kitty + `emoji_images=on`; runs otherwise. |
| `internal/image/fetcher.go` | Integration tests | URL-as-key dedup; singleflight on hot reactions; 404 negative caching; alias→404→glyph fallback chain. |
| `internal/ui/messages/`, `thread/` | Integration tests | Rendered output has expected 2-cell width per emoji; visible byte stream contains the kitty transmission once even for N appearances; cold-cache placeholder; re-render on `ImageReadyMsg`. |
| `internal/ui/reactionpicker/`, autocomplete | Integration tests | Picker grid and dropdown render with image tokens; selection and keyboard navigation unchanged. |
| Yank / clipboard | Integration test | Selection of emoji-containing messages yields `plainText`, never kitty placeholder runes. |
| Search | Integration test | Searching `thumbsup` matches messages containing `:thumbsup:` regardless of render form. |
| Manual smoke | Checklist | Kitty + plain shell; kitty + tmux; ghostty; WezTerm. CDN-blocked network → `emoji_images=off` fallback. `emoji_cells=1` layout consistency. |

## Verification Criteria

1. On kitty: every emoji that today falls back to `:name:` text now renders as an image. Spot-checks: `:flag-us:`, `:thumbsup::skin-tone-3:`, `:man_astronaut:`, `:rainbow-flag:`.
2. On kitty: workspace custom emoji render as their workspace artwork in every in-scope surface.
3. On kitty: layout alignment is byte-for-byte deterministic across kitty, ghostty, and WezTerm for a given message (no terminal-dependent column drift).
4. Yanked text from emoji-containing messages produces the same plain-text representation as today.
5. On `emoji_images=off` or non-kitty: behavior is byte-for-byte identical to current `main` (regression-tested via golden output of representative messages).
6. Startup probe is skipped on kitty + `emoji_images=on` (confirmed via debuglog).
7. `Fetcher.decoded` and `Fetcher.prerendered` map sizes stay bounded to the active workspace's emoji set after a long session.

## Open Questions Resolved During Implementation

- ~~Exact VS16-stripping rules for URL building~~ — resolved: VS16 is NOT stripped. Slack's CDN preserves every codepoint in the URL path. Locked in by `internal/emoji/testdata/slack_urls.json` and the HTTP probe described in `internal/emoji/url.go`.
- Empirical perf of N≫10 placements per frame. The unicode-placeholder mode is designed for this, but benchmark on a heavy channel anyway; stagger transmits if transmission throughput is the bottleneck.
- Whether the picker grid needs prefetch-on-open. Lazy is fine per the cold-cache UX choice; revisit if open-to-render latency is visibly bad.

## Out of Scope (Earmarked Follow-Ups)

- Persisted decoded-pixel cache on disk.
- Persisted kitty payload with stable cross-session image IDs.
- Per-workspace emoji style detection (Apple / Google / Twitter / Slack-classic).
- Surfaces G (channel list) and H (user status / display name).
- Picker / autocomplete prefetch tuning if lazy proves slow.
- Eventual deletion of the non-kitty rendering paths.
