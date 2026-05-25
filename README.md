# slk

> **A blazingly fast Slack TUI.**
> Keyboard-driven, beautifully themed, and under 20MB. One static binary. No Electron required.
>
> Marketing site: [getslk.sh](https://getslk.sh) · Docs: [Wiki](https://github.com/gammons/slk/wiki)

![slk screenshot](docs/assets/screenshot.png)

`slk` is a daily-driver replacement for the official Slack desktop client, built in Go with [bubbletea](https://github.com/charmbracelet/bubbletea) and [lipgloss](https://github.com/charmbracelet/lipgloss).

## Why slk?

- **Fast.** Cold start in milliseconds. Render-cached messages. SQLite-backed scrollback. Real-time over WebSocket.
- **Tiny.** ~19 MB on disk. ~60 MB RSS for a live multi-workspace session vs. 500 MB–1.5 GB for the official client. No node_modules, no Chromium, no 1Gb RAM tax.
- **Keyboard-first.** Vim-style modal editing. `j/k`, `h/l`, `i`, `Esc`.
- **Pretty.** 35+ built-in themes, lipgloss-styled panels, true-pixel avatars on kitty (half-block fallback elsewhere), emoji shortcodes, day separators, and pill-style reactions.
- **Multi-workspace.** All your workspaces stay connected in parallel. `1`–`9` to instantly jump between them, with live unread badges in the rail.
- **Yours.** TOML config, custom themes, custom channel sections via glob, XDG-compliant paths.

## Highlights

- Real-time messages, edits, deletes, reactions, typing indicators
- Inline images (kitty graphics / sixel / half-block fallback) with full-screen preview
- Threads side panel + a workspace-wide threads view
- Smart paste: clipboard images, file paths, or text — multiple attachments + caption in one send
- Slack-native sidebar sections, kept live; or glob-based config sections
- Browser-cookie auth (`xoxc` + `d`) — no Slack App required
- Vim-style modal keybindings, fuzzy channel finder, workspace picker
- 12 themes + drop-in custom themes, live theme switcher
- OS desktop notifications on DMs, mentions, and configurable keywords

Full feature breakdown: **[[Features|https://github.com/gammons/slk/wiki/Features]]**

## Quick install

**Homebrew** (macOS and Linux):

```bash
brew install gammons/tap/slk
```

**Linux/macOS tarball** (auto-resolves the latest version):

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/gammons/slk/releases/latest | grep -oE '"tag_name": *"v[^"]+"' | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | sed 's/^v//')
# Linux x86_64
curl -fsSL "https://github.com/gammons/slk/releases/latest/download/slk_${VERSION}_linux_x86_64.tar.gz" | tar xz
# macOS Apple Silicon
curl -fsSL "https://github.com/gammons/slk/releases/latest/download/slk_${VERSION}_darwin_arm64.tar.gz" | tar xz
sudo mv slk /usr/local/bin/
```

**Go:**

```bash
go install github.com/gammons/slk/cmd/slk@latest
```

For `.deb` / `.rpm` / `.apk` packages, Windows, build-from-source, and checksums, see the [Installation wiki page](https://github.com/gammons/slk/wiki/Installation).

## Setup

slk uses your existing browser session. Grab your `xoxc` token and `d`
cookie from DevTools, then:

```bash
slk --add-workspace
```

Full walkthrough: [Setup wiki page](https://github.com/gammons/slk/wiki/Setup).

## Enterprise Grid

slk authenticates via the same `xoxc` browser session token and `d`
cookie that `app.slack.com` uses. Every outbound request to `*.slack.com`
is decorated with browser-like headers (`User-Agent`, `Origin`,
`Referer`, `Sec-Fetch-*`) so Slack's anomaly detectors should treat the
traffic the same way they treat the browser tab the token was extracted
from.

This is best-effort mitigation, not a contract. Your IT policy may still
flag slk regardless. If you're on Enterprise Grid and slk signs you out
or triggers a security email after login, please file an issue with:

1. The exact email or notification text Slack sent.
2. Whether you got logged out of *all* sessions or only slk's.
3. The output of `slk --version`.

See [#5](https://github.com/gammons/slk/issues/5) for history.

## Inline images in tmux

If you run `slk` inside tmux on a Kitty-capable terminal (Kitty, Ghostty,
WezTerm), images render natively as long as tmux passthrough is enabled:

```tmux
set -g allow-passthrough on
```

Reload tmux for the setting to take effect (`tmux kill-server`, then
reattach). Verify with:

```bash
tmux show -gv allow-passthrough
```

Expected output: `on` (or `all`).

If passthrough is off, `slk` detects this at startup and falls back to
half-block rendering automatically — no config change needed. To force a
specific renderer regardless of detection, set `image_protocol` in
`config.toml` to `kitty`, `sixel`, `halfblock`, or `off`.

## Unread indicator in tmux

`slk` sets the terminal window title to reflect unread state — for
example `slk SW (3) +1` means three channels-with-unreads in the active
workspace and at least one other workspace also has unreads. The
two-letter prefix is the active workspace's initials (matching the
left-rail label).

Outside tmux this just works — modern terminals (Kitty, WezTerm,
Alacritty, Ghostty, iTerm2, Windows Terminal, gnome-terminal) render
title changes in their tab/window chrome.

Inside tmux there's an extra step. tmux intercepts the title escape
from slk, and only re-emits it to the outer terminal when title
forwarding is on, *and* it uses its own title template by default
(`#W` = window name) rather than the pane's title. Add both lines to
`~/.tmux.conf`:

```tmux
set -g set-titles on
set -g set-titles-string '#T'
```

`#T` (active pane title) is what carries slk's string. Reload tmux for
the setting to take effect (`tmux kill-server`, then reattach). Verify
with:

```bash
tmux show -gv set-titles
tmux show -gv set-titles-string
```

Expected output: `on` and `#T`.

If you'd prefer slk's unread indicator to work in tmux without any
config change at all, that's tracked as a follow-up — it requires
passing the title escape through tmux's DCS passthrough rather than
relying on `set-titles`. Not in this release.

## Debugging

Set `SLK_DEBUG=1` to enable a comprehensive debug log written to
`slk-debug.log` in the current working directory. The file is
**truncated each run**, so reproduce the issue, quit slk, then copy
the file before relaunching. Log lines are categorized
(`[cache]`, `[imgfetch]`, `[imgrender]`, `[ws]`, `[general]`) so
`grep '\[cache\]' slk-debug.log` slices to one focus area.

## Documentation

Everything lives in the [**wiki**](https://github.com/gammons/slk/wiki):

- [Installation](https://github.com/gammons/slk/wiki/Installation) — prebuilt binaries, Go install, build from source
- [Setup](https://github.com/gammons/slk/wiki/Setup) — token extraction, adding workspaces
- [Features](https://github.com/gammons/slk/wiki/Features) — full feature breakdown
- [Keybindings](https://github.com/gammons/slk/wiki/Keybindings) — every key, every mode
- [Configuration](https://github.com/gammons/slk/wiki/Configuration) — `config.toml`, custom themes, XDG paths
- [Terminal Compatibility](https://github.com/gammons/slk/wiki/Terminal-Compatibility) — what each terminal supports
- [Clipboard and OSC 52](https://github.com/gammons/slk/wiki/Clipboard-and-OSC-52) — copy/paste setup notes
- [Tradeoffs and Non-Goals](https://github.com/gammons/slk/wiki/Tradeoffs-and-Non-Goals) — roadmap, caveats, TOS notice
- [Architecture](https://github.com/gammons/slk/wiki/Architecture) — service layout, data layer

## Disclaimer

`slk` is an independent, unofficial project. It is not affiliated with, endorsed by, or sponsored by Slack Technologies, LLC or Salesforce, Inc. "Slack" is a trademark of Slack Technologies, LLC; it is used here only to describe the service this client interoperates with.

slk talks to Slack via the same internal browser protocol the official web client uses. This is unofficial and not sanctioned by Slack — see [Tradeoffs and Non-Goals](https://github.com/gammons/slk/wiki/Tradeoffs-and-Non-Goals#unofficial--tos-caveat) for details.

## License

[MIT](LICENSE) © Grant Ammons
