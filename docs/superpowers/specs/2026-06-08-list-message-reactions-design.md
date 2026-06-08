# List reactions on a message (issue #48)

## Problem

Reactions can be used for planning and decision making, so users want to be
able to see, for a given message, which users reacted and with what. Today the
app renders reaction pills (emoji + count) but provides no way to see *who*
reacted.

GitHub issue: #48 — "Add ability to list the reactions to a message".

## Goal

A read-only modal that lists every reaction on the selected message, grouped by
emoji, showing the display names of the users who reacted with each emoji.

## Key finding

The per-user reaction data already exists locally. `cache.ReactionRow`
(`internal/cache/reactions.go:8-12`) carries `UserIDs`, populated from the Slack
`conversations.history`/`replies` responses and kept up to date by the
`OnReactionAdded` / `OnReactionRemoved` websocket handlers
(`cmd/slk/main.go`). The UI model `messages.ReactionItem`
(`internal/ui/messages/model.go:79-83`) currently drops `UserIDs`, keeping only
emoji/count/`HasReacted`. So this feature is primarily plumbing + a new view; no
new Slack API surface is required.

## Non-goals

- No fresh `reactions.get` API call. Data is cache-only.
- No actions inside the modal (read-only). It does not add/remove reactions.
- No async name resolution work for now. Unresolved user IDs fall back to the
  raw ID via the existing `userNameFor` helper. Revisit only if it proves to be
  a problem in practice.

## Trigger & behavior

- **Key:** `L` (uppercase) in normal mode. Mnemonic "List reactions".
- **Main message pane:** acts on the currently selected message.
- **Thread pane:** acts on the currently selected reply (whichever pane has
  focus), consistent with how `r`/`R`/`E`/`D` already work in both panes.
- **No reactions on the message:** pressing `L` is a no-op; the modal does not
  open. Keeps the UX quiet rather than showing an empty box.
- **Toggle/close:** `esc` closes; pressing `L` again while open also closes.

## Data flow

1. On open, read reactions for the message from the cache via
   `cache.GetReactions(messageTS, channelID)`, returning
   `ReactionRow{Emoji, UserIDs, Count}`.
2. Resolve each `UserID` to a name using the existing `App.userNameFor`
   (`internal/ui/app.go:1476`), which falls back to the raw ID when unresolved.
3. Mark the current user's own entry as `(you)` using the client's user ID.
4. No network calls; no additions to the `SlackAPI` interface.

## UI / layout

A centered dimmed overlay modeled on the existing reaction picker
(`internal/ui/reactionpicker/model.go`), reusing `overlay.DimmedOverlay`.
Content is grouped by emoji with a per-emoji count and is scrollable when long:

```
 Reactions
 ──────────────────────
 :thumbsup:  (3)
   Alice Smith
   Bob Jones
   You (you)
 :eyes:  (1)
   Carol Lee
 ──────────────────────
 ↑/↓ scroll · esc close
```

- Emojis are rendered the same way the existing reaction pills render them.
- Scroll with `↑/↓` and `j/k`; close with `esc` (or `L` toggles closed).
- Read-only: no actions from within the modal.

## Code structure

New package and touched files:

- **New** `internal/ui/reactionsview/` — `Model` exposing
  `Open(...)`, `Close()`, `IsVisible()`, `HandleKey()`, `ViewOverlay()`.
  Mirrors the shape of `reactionpicker`.
- **`internal/ui/mode.go`** — add a mode/overlay-visible state for the view,
  following the pattern used by other modals.
- **`internal/ui/keys.go`** — add a `ListReactions` binding bound to `L`.
- **`internal/ui/mode_normal.go`** — dispatch `L`: build grouped data from the
  selected message (main pane) or selected reply (thread pane) and open the
  modal; no-op when there are no reactions.
- **`internal/ui/app.go`** — add a `reactionsView *reactionsview.Model` field
  and its construction; add a helper that assembles the grouped data (resolving
  names via `userNameFor` and marking the current user `(you)`).
- **`internal/ui/view_overlays.go`** — register the modal in `applyOverlays`
  and `overlayActive`.
- **`internal/ui/help/`** — add `L` to the keybindings help list.

## Testing

- Unit-test the data-assembly helper: given cache rows + a name map + the
  current user ID, it produces correctly grouped/ordered entries, marks `(you)`
  for the current user, and falls back to raw IDs for unresolved names.
- Unit-test the modal `Model`: open/close/visibility, scroll bounds, and
  no-op/empty-input behavior.
- Follow the test patterns established by `reactionpicker` and the other modal
  packages.
