# Mention Picker Channel Membership Design

Date: 2026-05-20

## Problem

The `@`-mention picker today shows the entire workspace user directory, undifferentiated. Two consequences for users:

1. People who are **not in the current channel** are mixed in with channel members. Mentioning them is technically allowed but is usually a mistake; the picker offers no signal that the person can't see the channel.
2. **External users** (Slack Connect / shared channels) never appear in the picker at all. They're not in `users.list` for our workspace, so `wctx.UserNames` never contains them, so they're invisible to the picker even when they're sitting in the same shared channel.

Additionally, every non-special row currently renders with a trailing empty parenthetical — e.g. `jane.doe ()` — because `mentionpicker.User.Username` is always blank in production. That's a pre-existing rendering bug we'll fix in the same change since we're already in the rendering code.

## Scope

In scope:

- Channel membership awareness in the mention picker: muted text + `(not in channel)` suffix for non-members, members-first sort.
- External user discovery and display: external users from shared channels appear in the picker with an `(ext)` suffix.
- Fix the dead empty-parens rendering for users with no Username.

Out of scope:

- Member-search beyond prefix matching (no fuzzy / substring).
- Recency-weighted sorting of the picker results.
- Showing channel member counts anywhere in the UI.
- Special handling for Enterprise Grid org-shared channels (should work via the same paths, but not specifically designed around).
- Filtering bots out of the picker (their `(not in channel)` status will be accurate; deliberate decision to leave behavior unchanged).

## Design Decisions

1. **Show everyone, sort members first.** Non-members aren't hidden or filtered out — they're sorted below in-channel users, in muted text, with a `(not in channel)` label. Preserves the "I can still mention them if I really want to" affordance while making accidental mentions obvious.

2. **Eager fetch on channel switch with SQLite persistence and live event updates.** Channel membership is fetched via `conversations.members` on first visit (and after TTL/reconnect invalidation), persisted to SQLite, and kept fresh by handling Slack's `member_joined_channel` and `member_left_channel` events. Reconnects invalidate the active channel; a 24h TTL is the backstop for everything else.

3. **External users are resolved at fetch time, not lazily.** When `conversations.members` returns a user ID we don't have, we resolve it via `users.info` immediately and cache the result. The picker never shows raw user IDs; users that fail to resolve are simply omitted.

4. **No new color dimension.** External users get a `(ext)` suffix in muted text rather than a distinct accent color. Keeps the visual language to two states (in-channel = normal, not-in-channel = muted), with optional muted suffixes layered on.

## Data Model

### `mentionpicker.User` — extend struct

In `internal/ui/mentionpicker/model.go`:

```go
type User struct {
    ID          string
    DisplayName string
    Username    string
    InChannel   bool   // false for non-members of the active channel
    IsExternal  bool   // true for Slack Connect users from another workspace
}
```

`InChannel` defaults to `true` for the special mentions (`@here`, `@channel`, `@everyone`). Until membership data lands for the active channel, all real users default to `InChannel = true` so the picker renders the same as today.

### SQLite — new `channel_members` table

```sql
CREATE TABLE channel_members (
    workspace_id TEXT NOT NULL,
    channel_id   TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, channel_id, user_id)
);
CREATE INDEX idx_channel_members_channel ON channel_members(workspace_id, channel_id);
```

One row per (channel, member). Single-row inserts/deletes for join/leave events. No JSON blobs.

### SQLite — new `channel_membership_meta` table

```sql
CREATE TABLE channel_membership_meta (
    workspace_id        TEXT NOT NULL,
    channel_id          TEXT NOT NULL,
    last_full_fetch_at  INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, channel_id)
);
```

`last_full_fetch_at` drives the 24h TTL check. Updated only on full successful paginated fetch — never on event deltas.

### `cache.User` — add `is_external` column

In `internal/cache/users.go`, add a nullable column:

```sql
ALTER TABLE users ADD COLUMN is_external INTEGER NOT NULL DEFAULT 0;
```

We persist external-user resolution so we don't re-resolve every time. Inferred from `team_id != client.TeamID()` in the `users.info` response.

No new fields on the `channels` table — we infer "shared channel" implicitly by whether any member resolves as external.

## Fetch & Cache Lifecycle

### New component: `internal/slack/membership.Manager`

One instance per workspace (constructed alongside `WorkspaceContext`). Owns:

- In-memory `map[channelID]*channelMembership` cache.
- A per-channel in-flight sentinel to dedupe concurrent fetches.
- A small `users.info` worker pool (max 4 concurrent) for external user resolution.

Backed by `cache.DB` for persistence.

### Lifecycle events

1. **Channel switch → `Manager.EnsureFresh(channelID)`**
   - Load from SQLite into memory if not already loaded.
   - Check `last_full_fetch_at`:
     - **Fresh (< 24h):** push current membership to the UI, done.
     - **Stale or missing:** push current membership to UI immediately (avoids empty picker), then kick off a background fetch.

2. **Background fetch** — paginated `client.GetUsersInConversation(channelID)` with `limit=1000` per page. After each page:
   - Diff against in-memory set; write inserts/deletes to SQLite.
   - For unknown user IDs (not in `wctx.UserNames`, not in `cache.User`), enqueue external resolution.
   - Push incremental update to UI if the channel is still active.

   On full completion, update `last_full_fetch_at`. If the active channel changed mid-fetch, finish writing to SQLite anyway; skip the final UI push.

3. **External user resolution** — worker pool reads from a dedupe-queue of `(workspace_id, user_id)` pairs.
   - Call `users.info`.
   - If `team_id != client.TeamID()`, set `is_external = 1`.
   - Write to `cache.User`, update `wctx.UserNames`, push to active picker.

4. **Live events** — wire `member_joined_channel` and `member_left_channel` into the existing event dispatcher.
   - Single-row insert/delete to SQLite + in-memory set.
   - If joining user is unknown, enqueue external resolution.
   - Push update to active picker if the event's channel is currently active.
   - Do **not** update `last_full_fetch_at` on event deltas — that timestamp tracks full-fetch freshness only.

5. **Websocket reconnect** — hook the existing reconnect path.
   - Force-stale the currently active channel.
   - Immediately call `EnsureFresh(activeChannelID)`.
   - Other channels stay as-is; they re-validate on next visit.

### UI bridge

New methods on `App`:

- `App.SetChannelMembership(channelID string, memberIDs []string)` — analogous to existing `App.SetUserNames`. Forwards to both compose pickers. The payload is just user IDs; the picker resolves names from the workspace user list.
- `compose.SetActiveChannel(channelID string)` — tells the picker which channel context to use when computing `InChannel`.

The picker keeps two pieces of state:

- Full workspace user list (from `SetUsers`, same as today; now includes resolved external users from any shared channels we've visited).
- Per-channel member ID set (from `SetChannelMembership`).

`InChannel` and `IsExternal` are populated on the `User` slice handed to the picker, computed per channel switch / membership update:

- `InChannel = userID ∈ memberIDs`.
- `IsExternal = cache.User.IsExternal` for that user.
- **External user visibility rule:** a user with `IsExternal = true` is **only included in the picker's user list when they are in the active channel** (i.e., when `InChannel` would be true). External users who were cached from a previous shared channel but aren't in the current channel are filtered out of the picker entirely. This avoids the misleading `(ext) (not in channel)` combination — you can't usefully mention an external user from a channel they're not in. Internal users follow the existing "show with `(not in channel)` label" rule when not in the active channel.

## UI Rendering & Sort

### Sort order (top to bottom)

1. Special mentions matching the query: `@here`, `@channel`, `@everyone`.
2. In-channel users matching the query, alphabetical by DisplayName (internal + external interleaved).
3. Not-in-channel users matching the query, alphabetical by DisplayName.

Tie-break is alphabetical. No recency or relevance signal today; can be added later without breaking the protocol.

### `MaxVisible`

Bump from 5 to **7**. Still fits comfortably above the compose box, gives room for the new mixed-class rows.

### Row format

Example query `@j` in a shared channel:

```
▌ jane.doe                       ← selected, in channel, internal
  john.smith                     ← in channel, internal
  jenny.kim (ext)                ← in channel, external
  jordan.lee (not in channel)    ← not in channel, internal, ENTIRE ROW MUTED
  jamal.foo (not in channel)     ← not in channel, internal, ENTIRE ROW MUTED
```

### Styles

Using existing styles from `internal/ui/styles/styles.go`:

| Row class | DisplayName color | Suffix | Suffix color |
|---|---|---|---|
| In channel, internal | `TextPrimary` | — | — |
| In channel, external | `TextPrimary` | ` (ext)` | `TextMuted` |
| Not in channel | `TextMuted` | ` (not in channel)` | `TextMuted` |
| Special mention | `TextPrimary` | ` (Username)` | `TextPrimary` |

### Selection treatment

- Indicator `▌` in `Accent` (unchanged).
- In-channel rows: DisplayName becomes **bold** (unchanged).
- Not-in-channel rows: DisplayName becomes **bold + `TextPrimary`** when selected (upgrades from muted), but ` (not in channel)` suffix stays `TextMuted`. The focused row stays readable; the at-rest visual hierarchy is preserved on unselected rows.

### Dead-code fix for empty parens

Current at `internal/ui/mentionpicker/model.go:148`:

```go
label := fmt.Sprintf("%s (%s)", u.DisplayName, u.Username)
```

Replace with:

```go
label := u.DisplayName
if u.Username != "" {
    label = fmt.Sprintf("%s (%s)", u.DisplayName, u.Username)
}
```

`(Username)` disappears for real users (where Username is empty) and is preserved for the three special mentions which legitimately set it. The `(ext)` and `(not in channel)` suffixes are appended afterward, independent of this conditional.

### Filter logic

Unchanged from today: prefix match on DisplayName and Username, accent-folded via `text.Fold`. The match domain still includes all users (in-channel and not). Sort the matched set per the order above, then apply `MaxVisible`.

### Loading state

When membership data hasn't arrived for the active channel (first switch, cache miss in-flight), every real user defaults to `InChannel = true` — picker renders the same as today. When membership data lands, picker re-renders with proper labels. No spinner; one-frame re-sort.

### Width handling

Long `DisplayName + (not in channel)` may overflow narrow terminals. The picker's existing width plumbing truncates row-level, so the suffix truncates with the rest. Acceptable — matches current behavior.

## Failure Modes & Edge Cases

**API failures:**

- `conversations.members` error (network, 5xx, permission): log it, leave `last_full_fetch_at` unchanged so we retry on next visit. Picker silently degrades to "no membership data" mode (everyone shown without labels).
- Slack 429 rate-limit: respect `Retry-After` via slack-go's built-in handling. Background fetch sleeps and resumes.
- `users.info` fails for an external user: leave them unresolved. Picker **does not show** unresolved users (raw `Uxxxx` IDs would be worse than omission). Resolution is retried on next channel visit.

**Drift:**

Live events keep things current while connected. Reconnects refresh the active channel. 24h TTL is the offline-drift backstop. Up to 24h of staleness for non-active channels after long offline periods is acceptable.

**Picker open during channel switch:**

The picker is owned by `compose.Model`, which is tied to the active channel. Channel switch dismisses the picker as a side effect of context change. The new channel's membership is loaded via `EnsureFresh` as part of the switch handler. **Verify during implementation:** if the picker can persist across channel switches today, explicitly close it on switch.

**DMs and group DMs:**

`conversations.members` works on `D…` and `G…` IDs and returns implicit member lists. Treated like any other channel — no special-case code. A 1:1 DM will show 2 members in-channel and everyone else as "not in channel"; that's accurate, if visually unusual.

**Workspace switch:**

`Manager` is per-workspace. SQLite is workspace-scoped via the `workspace_id` column. Switching workspaces switches manager instances.

**Channels we're not a member of:**

User can't open a compose box for unjoined channels, so `EnsureFresh` is never called for them. No design change needed.

**Migration failure:**

Tables are additive — new `channel_members`, new `channel_membership_meta`, new `is_external` column with default. If migration fails, follow the existing `internal/cache/` migration error pattern. Recoverable by deleting the cache file.

**Rapid channel switching (A → B → A):**

In-flight sentinel prevents duplicate fetches per channel. Both fetches complete and write to SQLite. Only the currently active channel pushes UI updates. No race in visible state.

**Out-of-order events after reconnect:**

Events are applied as deltas — re-applying a duplicate join is idempotent (already in set), re-applying a duplicate leave on a missing user is a no-op. No correctness issue.

**Members we can't render a name for:**

A `member_joined_channel` event may name a user we've never seen and can't resolve (offline, `users.info` fails, deleted account, etc.). Such users sit in `channel_members` but aren't in `cache.User` / `wctx.UserNames`. The picker skips them — better to show nothing than `U07ABCDE`. Resolution is retried opportunistically on next channel visit. If they're ever rendered without a name in the message history, that's a separate codepath (the existing `UserResolver` in `cmd/slk/main.go`) and not affected by this design.

## Touched Files

Reasonably complete list. Exact line numbers shift during implementation.

- `internal/ui/mentionpicker/model.go` — extend `User` struct, update filter sort, update view rendering (suffixes, muted styles, selection upgrade, empty-parens fix).
- `internal/ui/compose/model.go` — add `activeChannel`, accept and store channel membership, pass it into the picker, close picker on channel switch if needed.
- `internal/ui/app.go` — add `SetChannelMembership`, propagate to both compose pickers; thread `compose.SetActiveChannel` through channel-switch handlers.
- `internal/slack/membership/manager.go` — new file. `Manager`, `EnsureFresh`, background fetch goroutine, in-flight sentinel, external resolution worker pool.
- `internal/slack/client.go` — add `GetUsersInConversation` wrapper around slack-go's `GetUsersInConversationContext`.
- `internal/slack/events.go` — add `member_joined_channel` and `member_left_channel` cases to the dispatch switch (around line 258, alongside the existing `message`, `reaction_added`, `presence_change`, etc. cases).
- `internal/cache/migrations/` — new migration adding `channel_members`, `channel_membership_meta`, and the `is_external` column on `users`.
- `internal/cache/users.go` — add `IsExternal` to the `User` struct and queries.
- `internal/cache/channel_members.go` — new file. Insert / delete / list / TTL-meta queries.
- `cmd/slk/main.go` — construct `Manager`, wire reconnect-refresh hook, wire channel-switch hook.

## Testing Strategy

- **Unit tests** on `Manager`:
  - `EnsureFresh` cache hit (fresh) — no fetch.
  - `EnsureFresh` cache miss — fetch + persist + UI push.
  - `EnsureFresh` stale (past TTL) — fetch + persist + UI push.
  - Concurrent `EnsureFresh` for same channel — single fetch (in-flight sentinel).
  - Event handler: join applies delta without touching `last_full_fetch_at`.
  - External user resolution: unknown ID → `users.info` → cache write.
  - External user resolution failure: ID stays unresolved, not shown.
- **Unit tests** on `mentionpicker` sort/render:
  - Sort order with mixed in-channel / not-in-channel / external / special rows.
  - Empty-parens fix: non-special row without Username renders no parenthetical.
  - Selection upgrade: muted-row-when-selected renders bold + `TextPrimary` on name, muted on suffix.
  - Loading state: when no channel membership data, every real user renders as in-channel.
- **Integration / manual**:
  - Open a public channel, type `@` — see members at top, non-members muted below.
  - Open a Slack Connect channel, type `@` — see external members with `(ext)` suffix.
  - Disconnect and reconnect — verify active channel re-fetches and picker reflects current membership.
  - Receive a `member_joined_channel` event while picker is open — verify joined user moves to in-channel section on next render.
