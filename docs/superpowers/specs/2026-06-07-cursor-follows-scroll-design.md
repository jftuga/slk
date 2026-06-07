# Cursor Follows Scrolling (clamp-to-edge)

## Problem

In the messages pane, scroll position (`yOffset`) and the selected-message
cursor (`selected`) are deliberately decoupled. Mouse-wheel and page/half-page
scrolling move the viewport while leaving the selection pinned, so the selected
message can scroll off-screen and stay there.

We want the selected cursor to **follow** the scroll: when the user scrolls up
or down, the cursor should stay within the visible area.

## Behavior

**Clamp-to-edge.** The selection stays put while it remains on-screen. It only
moves when scrolling would push it off the top or bottom edge, in which case it
sticks to the nearest still-visible message. "Visible" means the message has at
least one line inside the viewport (partial visibility counts).

Applies to **both** scroll inputs:
- mouse wheel (`reducer_mouse.go` → `ScrollUp`/`ScrollDown`)
- page / half-page keys (`scrollFocusedPanel` in `app.go` → `ScrollUp`/`ScrollDown`)

Out of scope: sidebar and thread-panel scrolling are unchanged.

## Implementation

Single new step in `Model.viewInternal` (`internal/ui/messages/model.go`),
inserted after the final `yOffset` clamp (currently model.go:2794-2803) and
before the visible-window build loop (currently model.go:2855).

The selected entry's line range (`selectedStartLine`/`selectedEndLine`) is
already computed earlier in `viewInternal` (model.go:2763-2778). After the
final `yOffset` is known, compute the visible window
`[yOffset, yOffset+msgAreaHeight)` and:

- If the selection is entirely above the window
  (`selectedEndLine <= yOffset`): set `selected` to the **topmost** message
  entry that is at least partially visible.
- If the selection is entirely below the window
  (`selectedStartLine >= yOffset+msgAreaHeight`): set `selected` to the
  **bottommost** partially-visible message entry.

Determine the topmost/bottommost visible message by scanning `m.cache` /
`m.entryOffsets` for entries with `msgIdx >= 0` (skip date separators) whose
range `[entryStart, entryStart+height)` intersects the visible window.

When `selected` changes:
- set `m.snappedSelection = m.selected` (keep the snap guard consistent so the
  selection-snap branch doesn't fight the clamp on the next render),
- recompute `selectedStartLine`/`selectedEndLine` for the new selection so the
  render loop picks the correct selected-variant lines and highlight in the
  same frame.

### Why this location

- It runs after `yOffset` is finalized (`ScrollDown` defers its clamp to
  `viewInternal`).
- It runs against the rebuilt `cache`/`entryOffsets`.
- It is a no-op when the selection is already visible, so it can run on every
  render. Both scroll paths already set the snap guard, so no input-handler
  changes are required.

### Interaction with existing snap logic

- `MoveUp`/`MoveDown` change `selected` so `snappedSelection != selected`; the
  existing snap branch (model.go:2784) snaps `yOffset` to the selection, making
  it visible — the new clamp is then a no-op. No conflict.
- When the selected message is taller than the viewport, the snap branch leaves
  it partially visible (top above the fold, bottom at the edge); the clamp
  treats it as visible and does nothing.

## Cleanup

Update stale doc comments that state scrolling leaves the selection unchanged:
- `ScrollUp` / `ScrollDown` (model.go:852-872)
- `scrollFocusedPanel` (app.go)
- the mouse-wheel reducer (reducer_mouse.go)

Note: `ViewportAtTop()` (yOffset-based) backfill still works; with the cursor
following the scroll, reaching the top also drives `AtTop()` (selection-based).

## Testing

- Update `TestScrollPreservedAcrossRenders` (model_test.go:133-166): `yOffset`
  preservation still holds; the assertion about `selected` staying off-screen
  changes — `selected` now clamps to the visible edge.
- Add a test: with a selection on-screen, scrolling down far enough to push it
  off the top clamps `selected` to the topmost visible message; scrolling up
  clamps to the bottommost visible message; scrolling that keeps the selection
  on-screen leaves `selected` unchanged.
