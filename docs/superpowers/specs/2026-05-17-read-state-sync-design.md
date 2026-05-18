# Read-State Sync Rewrite Design

## Motivation

After shipping v0.7.11 (which fixed silent message drops during reconnect backfill), user testing revealed a separate class of sync defects, visible side-by-side with the official Slack desktop client:

1. Channels show unread in the official client but NOT in slk.
2. Channels show as unread in slk after the user has read them in the official client.

Investigation showed these are not symptoms of the v0.7.11 follow-ups (`Capped` semantics on exact-`maxTotal`, `GetUnreadCounts` ignoring `ctx`). They are caused by a separate set of defects in how slk handles read state, all rooted in a fundamental architectural problem: **read state lives in three different stores that are kept in sync only partially and only by some code paths.**

## Root causes

Read state per channel is currently spread across three stores:

| Store | Fields | Written by |
|---|---|---|
| SQLite (`channels` table) | `last_read_ts`, `unread_count` | Bootstrap, `OnChannelMarked`, `markChannelReadAsync` |
| In-memory `WorkspaceContext` | `Channels[i].LastReadTS`, `Channels[i].UnreadCount`, `LastReadMap` | Bootstrap, inactive-workspace `OnMessage` bump |
| In-memory `sidebar` model | `items[i].UnreadCount` | `MarkUnread`/`ClearUnread`/`SetUnreadCount` from `App.Update` |

The sidebar dot — the user-visible signal of "has unreads" — is driven entirely by the third store (`internal/ui/sidebar/model.go:1151-1153`). The other two are inputs at different lifecycle moments, and there is no single mutation path that updates all three together.

Specific defects this produces:

- **`runChannelPhase` discards `UnreadInfo.LastRead`.** (`cmd/slk/reconnect_backfill.go:113-121`) On reconnect, slk pulls `client.counts` but only uses `HasUnread` to broaden the backfill set; the freshly-served `LastRead` value is thrown away. If the user read a channel in the official client during a disconnect, the `channel_marked` event was missed by slk and there is no catch-up — the channel stays unread in slk forever (until manually opened).
- **`UpsertChannel` clobbers read state via `ON CONFLICT DO UPDATE`.** (`internal/cache/channels.go:21-41`) Every channel re-upsert writes `last_read_ts` and `unread_count` to the zero values of the passed-in `cache.Channel` struct. Bootstrap (`upsertChannelInDB`, `cmd/slk/channelitem.go:101-109`) passes a struct without these fields set, wiping persisted read state. Restored immediately for channels that `client.counts` returns; permanently lost for channels it doesn't.
- **`UnreadCount` is set to `MentionCount` (capped to ≥1), not the actual unread count.** (`internal/slack/client.go:728-754`) Slack's response contains `unread_count_display` which is parsed and discarded. The persisted count is consistently wrong by 1+ for any channel with unreads but no @-mentions.
- **`db.UpdateUnreadCount` is declared but called from zero production sites.** The persisted column is frozen at bootstrap-time values.
- **`OnChannelMarked` updates the DB and `LastReadMap` but not `wctx.Channels[i]`.** A workspace switch reseeds the sidebar from `wctx.Channels` and the cleared unread reappears.
- **Active-workspace `OnMessage` mutations bypass `wctx.Channels`.** Live unread bumps go into `sidebar.items[i].UnreadCount`; `wctx.Channels[i].UnreadCount` is untouched. A workspace switch loses them.
- **No catch-up on reconnect for read state at all.** Reconnect backfills message content but does not re-pull `last_read` for any channel.

## Architectural decisions

### One source of truth: SQLite

Per-channel read state is canonically stored in SQLite. The in-memory `WorkspaceContext.LastReadMap` and the in-memory `ChannelItem.UnreadCount` are eliminated as separate stores. They are replaced by a single read path: a batched query at sidebar render time.

The DB is fast enough that per-render queries are not a concern: bubbletea only re-renders on `tea.Msg` arrivals (single-digit/sec for typical use), the query is a single batched lookup against the workspace-scoped index, and the SQLite WAL configuration already in place handles concurrent reads without blocking the writer.

### Boolean `has_unread`, not integer `unread_count`

The user-visible unread signal is a dot. The integer count is meaningful only for section aggregates (e.g., "DMs (N)"). After this redesign, those aggregates count channels-with-unreads instead of summing unread-message-counts. This is a deliberate user-visible behavior change.

Rationale:
- Slack's `client.counts` does not reliably provide a true unread count (`unread_count_display` is sparse and inconsistent across channel types).
- Maintaining an accurate integer count locally requires tracking every WS message increment/decrement, every read-mark reset, and reconciling against the server periodically. Each of those is a place for drift.
- A boolean has only two states and a single authority (Slack's `has_unreads`). It cannot drift in the same way.
- Section aggregates of "channels with unreads" are arguably more useful than "unread messages" anyway — the latter can grow into the hundreds and ceases to communicate urgency.

### Single mutation API

All read-state writes flow through one logical operation, exposed as two functions for the single-row and batched cases:

```go
// internal/cache/channels.go (or new file channels_read_state.go)

// UpdateChannelReadState atomically updates the per-channel read state.
// If lastReadTS == "", the existing last_read_ts is preserved (useful
// for events that update has_unread only, e.g. new-message arrivals).
// This is the ONLY function permitted to modify read state after bootstrap.
func (db *DB) UpdateChannelReadState(channelID, lastReadTS string, hasUnread bool) error

// BatchUpdateChannelReadState writes multiple updates in a single
// transaction. Used by bootstrap and reconnect catch-up paths.
func (db *DB) BatchUpdateChannelReadState(workspaceID string, updates []ChannelReadStateUpdate) error
```

And one read function:

```go
// GetWorkspaceReadState returns (channelID -> ReadState) for every
// channel in the workspace. Single batched query. Called by the
// sidebar View() at render time.
func (db *DB) GetWorkspaceReadState(workspaceID string) (map[string]ReadState, error)

type ReadState struct {
    LastReadTS string
    HasUnread  bool
}
```

## Schema changes

```sql
-- Migration via the existing addColumnIfMissing helper:
ALTER TABLE channels ADD COLUMN has_unread INTEGER NOT NULL DEFAULT 0
```

The existing `last_read_ts` column is kept as-is.

The existing `unread_count` column is **kept for one release** as deprecated, ignored by all read paths, never updated by any write path. Drop in a follow-up release.

The `cache.Channel` struct loses its `LastReadTS` and `UnreadCount` fields entirely. This is the type-system enforcement that prevents future code from bypassing the read-state API.

## Data inflows

The six (and only six) sources of read-state mutation:

| Source | Call site | New write |
|---|---|---|
| Bootstrap | `connectWorkspace` (after `GetUnreadCounts`) | `BatchUpdateChannelReadState` for all channels in response |
| Reconnect catch-up | `runChannelPhase` (after `GetUnreadCounts`) | `BatchUpdateChannelReadState` for all channels in response (including `HasUnread=false`) |
| WS `*_marked` events | `OnChannelMarked` handler | `UpdateChannelReadState(id, eventTS, false)` |
| New WS message, inactive channel | `OnMessage` handler | `UpdateChannelReadState(id, "", true)` |
| User opens channel in slk | `markChannelReadAsync` (after server ack) | `UpdateChannelReadState(id, latestTS, false)` |
| User presses `U` | `MarkChannelUnread` flow (after server ack) | `UpdateChannelReadState(id, boundaryTS, true)` |

`UpsertChannel` is fixed to never touch `has_unread` or `last_read_ts` (they are removed from the `ON CONFLICT DO UPDATE` clause and from the `cache.Channel` struct).

`wctx.LastReadMap` and the in-memory `Channels[i].LastReadTS`/`UnreadCount` fields are removed. Every consumer goes through the DB.

`sidebar.ChannelItem.UnreadCount` is removed. The sidebar's `View()` calls `GetWorkspaceReadState` once per render and looks up `HasUnread` per item.

`sidebar.MarkUnread` / `ClearUnread` / `SetUnreadCount` are deleted. The `App.Update` handlers that called them either (a) call `UpdateChannelReadState` directly (writing to DB triggers a re-render via a `ReadStateChangedMsg`), or (b) are deleted because their work moved into the WS handler.

## Data outflows

- **Sidebar dot.** `View()` reads from `GetWorkspaceReadState`. Dot rendered when `state.HasUnread && !item.IsMuted`. No internal state.
- **Section aggregate.** Counts channels-with-unreads. Single-line change in `aggregateUnreadForSection`.
- **Message pane "new messages" line.** Already reads `lastReadTS` from a per-channel call. Unaffected by the consolidation other than the call going through `GetChannelReadState(channelID)` instead of `wctx.LastReadMap`.
- **Workspace rail dot.** Becomes "any channel in this workspace has `has_unread`." Implemented as a new cache function `db.WorkspacesWithUnreads() ([]string, error)` that returns the set of workspace IDs with at least one `has_unread=true` channel (single `SELECT DISTINCT workspace_id FROM channels WHERE has_unread = 1` query). Called by the rail's render path. Not derived from `GetWorkspaceReadState` because the rail needs cross-workspace data, not just the active workspace's.
- **Notifications.** Unaffected — notification gating uses `notify.NotifyContext`, not the unread store.

## Active-channel handling

When a WS `OnMessage` arrives for the channel the user is actively viewing, `has_unread` should NOT be set to true. The handler checks `h.activeChannelID()` and skips the write if it matches. The existing `markChannelReadAsync` flow continues to advance `last_read_ts` and clear `has_unread` to `false` when appropriate.

This is the same active-channel suppression logic as today, just relocated to `OnMessage` and routed through the single mutation API.

## Non-goals (explicit deferrals)

- **Periodic re-sync.** Considered and explicitly rejected. The WS event stream + reconnect catch-up should be sufficient. A periodic poll would mask real sync bugs rather than fix them. If we observe drift in production that the catch-up can't resolve, we diagnose the missing event path rather than paper over it with a timer.
- **Dropping the `unread_count` column.** Kept for one release as dead-but-harmless. Removal is a follow-up.
- **Restructuring the `OnMessage` handler beyond the active-channel check.** The handler does a lot (caching, notification, threading) and we don't touch the non-read-state branches.
- **Changing the user-facing keybindings or visual design** of the sidebar dot or section aggregates beyond the count-of-channels-with-unreads semantic.

## Test plan

The user has explicitly called out: structural changes that don't break tests mean the tests aren't testing the right thing. This branch should produce both compile failures (from the `cache.Channel` field removal) and assertion failures in tests that exercised the old store-divergence behavior.

### Tests expected to break (and require updates)

- `internal/cache/channels_test.go::TestUpsertChannel*` — any test verifying that `ON CONFLICT DO UPDATE` clobbers `last_read_ts` is now testing the opposite invariant.
- Any test constructing `cache.Channel{LastReadTS: "...", UnreadCount: N}` — compile failure (intentional).
- `internal/ui/sidebar/model_test.go` — tests that called `MarkUnread`/`ClearUnread`/`SetUnreadCount` and checked the resulting `items[i].UnreadCount` will need to either be deleted (those methods are gone) or rewritten as integration tests that drive the new `UpdateChannelReadState` API and verify the rendered dot.
- `cmd/slk/main_test.go` (if it tests `OnChannelMarked`) — verify the new DB-only write path.

### New tests required

**Cache layer (new file `internal/cache/channels_read_state_test.go`):**

- `UpdateChannelReadState` writes both columns correctly.
- `UpdateChannelReadState(id, "", true)` leaves `last_read_ts` unchanged.
- `UpdateChannelReadState` is idempotent.
- `BatchUpdateChannelReadState` is transactional (all-or-nothing on partial failure).
- `GetWorkspaceReadState` returns all channels including ones at the default `has_unread=false`.
- **Clobber regression**: `UpsertChannel` followed by `UpdateChannelReadState` followed by `UpsertChannel` — read state survives the second upsert. This is the test that proves the old bug is fixed.

**Event handlers (`cmd/slk/main_test.go` or a new file):**

- `OnChannelMarked` calls `UpdateChannelReadState(id, ts, false)` and does NOT mutate `wctx.Channels`.
- `OnMessage` on an inactive channel calls `UpdateChannelReadState(id, "", true)`.
- `OnMessage` on the active channel does NOT call `UpdateChannelReadState` (active-channel suppression).
- `OnMessage` for an inactive workspace: same path as inactive channel (no special inactive-workspace branch).

**Reconnect catch-up (`cmd/slk/reconnect_backfill_test.go`):**

- `runChannelPhase` calls `BatchUpdateChannelReadState` for every channel in the `GetUnreadCounts` response, including `HasUnread=false`. This is the test that proves Symptom 2 is fixed.

**Bootstrap (`cmd/slk/main_test.go` or new):**

- `connectWorkspace` populates both `has_unread` and `last_read_ts` for both `HasUnread=true` and `HasUnread=false` channels returned by `client.counts`.

**Sidebar render path:**

- `View()` calls `GetWorkspaceReadState` exactly once per render.
- Dot is rendered iff `state.HasUnread && !item.IsMuted`.
- Muted-but-unread channels: no dot.
- Channel not in the read-state map (race between channel creation and first read-state write): treated as `HasUnread=false` (no dot).

**Integration test (extend `TestBackfill_OvernightSuspendScenario`):**

Add a fourth channel category, "channel D": pre-suspend has unreads and a `last_read_ts`; during the offline window the user reads it in the official client. `client.counts` at reconnect returns `HasUnread=false` and a newer `LastRead` for D. After backfill, assertions:

- `db.GetChannelReadState("D").HasUnread == false`
- `db.GetChannelReadState("D").LastReadTS == newReadTS`

This is the test that locks in Symptom 2's fix end-to-end.

## Migration and rollout

- Branch off `main` (currently at v0.7.11).
- Land changes incrementally per the implementation plan.
- Ship as v0.7.12 (or v0.8.0 if we decide the user-visible aggregate change warrants a minor bump).
- On first launch after upgrade:
  1. New `has_unread` column exists with default `0` for every existing channel.
  2. WS reconnects fresh, `OnConnect` fires, `runChannelPhase` runs.
  3. `GetUnreadCounts` is called, `BatchUpdateChannelReadState` populates current state.
  4. Sidebar re-renders with correct dots.

The window of "post-upgrade, pre-first-reconnect-completion" — typically sub-second — shows no unread dots. Acceptable.

No user data is destroyed by the migration. Existing `last_read_ts` values are preserved (we only ADD `has_unread`).

## Out of scope for this branch (genuine follow-ups)

These items are not addressed by this design and remain as follow-ups, separate from the read-state work:

1. v0.7.11 follow-up: `GetHistorySince` flags `Capped: true` on exact-`maxTotal` even when no more pages remain. Symptom: wasted re-fetch.
2. v0.7.11 follow-up: `GetUnreadCounts` ignores `ctx`. Symptom: possible hang on shutdown.
3. Drop the deprecated `unread_count` column after one release.
4. Move the watermark helpers from `channels.go` to `channels_sync.go` for cohesion (noted in v0.7.11 reviews).

## Open questions

None. All design decisions are settled.
