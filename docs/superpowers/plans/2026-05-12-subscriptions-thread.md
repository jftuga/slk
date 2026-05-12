# Subscriptions.thread integration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace slk's heuristic "involved threads" view with Slack's authoritative `subscriptions.thread` state so the threads pane matches what the official Slack client shows.

**Architecture:**

1. New cache table `thread_subscriptions` mirrors Slack's per-thread subscription rows (workspace, channel, thread_ts, last_read, active).
2. New Slack-client method `ListThreadSubscriptions` calls Slack's internal `subscriptions.thread.list` endpoint (exact name confirmed in Task 1).
3. The existing `thread_marked` WS event handler (`rtmEventHandler.OnThreadMarked`) gains a side-effect: upsert the corresponding `thread_subscriptions` row so in-session state stays current.
4. The reconnect backfiller gains a third phase, `runSubscriptionPhase`, that fetches the full subscription list, reconciles the local table, then fetches parents for any subscribed thread whose parent message isn't in the cache.
5. `ListInvolvedThreads` is renamed to `ListSubscribedThreads`. The SQL now joins `thread_subscriptions` instead of pivoting off the `messages` heuristic, and `Unread` is computed against per-thread `last_read` instead of per-channel `last_read_ts`.
6. When `ListThreadSubscriptions` fails, the threads view shows a one-line "Threads list unavailable" banner driven by a new `WorkspaceContext.SubscriptionsAvailable` flag that flows through `ThreadsListLoadedMsg` into the threadsview model.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite` via `database/sql`), `slack-go/slack`, Bubble Tea / lipgloss for UI.

**Spec:** `docs/superpowers/specs/2026-05-12-subscriptions-thread-design.md`

---

## File map

**New files:**
- `internal/cache/thread_subscriptions.go` — `ThreadSubscription` struct, `UpsertThreadSubscription`, `DeleteThreadSubscription`, `ListActiveThreadSubscriptions`, `ReconcileThreadSubscriptions`.
- `internal/cache/thread_subscriptions_test.go` — tests for all five symbols above.
- `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md` — Task 1 discovery output. Subsequent tasks reference its contents.

**Modified files:**
- `internal/cache/db.go` — add `CREATE TABLE IF NOT EXISTS thread_subscriptions` plus the workspace/active index to the schema heredoc inside `migrate()`.
- `internal/cache/db_test.go` — assert the new table exists after `New`.
- `internal/cache/threads.go` — rename `ListInvolvedThreads` → `ListSubscribedThreads`; rewrite SQL to join `thread_subscriptions`; compute `Unread` from per-thread `last_read`. Keep `ThreadInvolvesUser` unchanged.
- `internal/cache/threads_test.go` — replace `TestListInvolvedThreads_*` with `TestListSubscribedThreads_*`. Keep `TestThreadInvolvesUser_*` unchanged.
- `internal/slack/client.go` — add `ListThreadSubscriptions(ctx)` method and supporting types/JSON structs; reuses the `postForm` + `truncateForLog` helpers already in the file.
- `internal/slack/client_test.go` — pagination, empty, hard cap, rate-limit-retry tests for `ListThreadSubscriptions`.
- `cmd/slk/main.go` — extend `OnThreadMarked` to upsert the subscription row; add `WorkspaceContext.SubscriptionsAvailable bool` field initialised to `true`; update the `SetThreadsListFetcher` closure to switch to `ListSubscribedThreads` and stamp the new flag onto `ThreadsListLoadedMsg`.
- `cmd/slk/event_handler_test.go` — add `TestOnThreadMarked_UpsertsSubscription`.
- `cmd/slk/reconnect_backfill.go` — extend `historyFetcher` interface with `ListThreadSubscriptions`; add `runSubscriptionPhase` between thread-phase and the `ThreadsListDirtyMsg` dispatch; flip `wctx.SubscriptionsAvailable` based on phase outcome (needs a write-through callback so the package doesn't import `cmd/slk` types).
- `cmd/slk/reconnect_backfill_test.go` — extend `fakeHistory` with a `ListThreadSubscriptions` method and add four new tests covering happy path, parent fetch for uncached threads, reconcile-on-unsubscribe, and error-flips-flag.
- `internal/ui/app.go` — add `SubscriptionsAvailable bool` field to `ThreadsListLoadedMsg`; the handler at `app.go:1936-1948` calls a new `threadsView.SetSubscriptionsAvailable(bool)` setter.
- `internal/ui/threadsview/model.go` — add a `subscriptionsAvailable bool` field (default `true`); add `SetSubscriptionsAvailable(bool)` setter; render a single-line banner above the rest of the view when the flag is `false`.
- `internal/ui/threadsview/model_test.go` — banner-visibility tests.

---

## Discovery output document

Task 1 produces a Markdown file at `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`. Every subsequent task that mentions discovery values is implicitly referring to that file. The file must contain at minimum these sections (filled in with concrete values, not placeholders):

```markdown
# subscriptions.thread.list — observed contract

## Endpoint
- Method:           POST
- URL path:         api/subscriptions.thread.list           (relative to https://<workspace>.slack.com/)
- Required headers: Authorization: Bearer <xoxc>, Cookie: d=<dxxx>

## Request form fields
- token             (xoxc)
- count             (page size; observed default 50)
- cursor            (empty on first page; opaque string on subsequent pages)
- ...               (any other observed fields)

## Pagination cursor location
response_metadata.next_cursor    (empty string when no more pages)

## Response shape (top-level)
{
  "ok": true,
  "subscriptions": [ ... ],
  "response_metadata": { "next_cursor": "..." }
}

## Per-subscription item shape
{
  "channel":   "C0123",
  "thread_ts": "1700000000.000100",
  "last_read": "1700000000.000200",
  "active":    true,
  ...
}

## Inactive subscriptions in response?
Yes / No — and the field used to distinguish them.

## Sample full response (one page)
<paste here>
```

If, during discovery, you find Slack uses a different field name (e.g. `entries` instead of `subscriptions`, or a different cursor location), record the actual values verbatim and the implementation tasks will follow them.

---

## Task 1: Endpoint discovery — DONE (notes file already present)

**Files:**
- Reference: `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`

Discovery was completed by the human operator during planning. The raw curl examples and full sample response were captured in the bottom of the spec file (`docs/superpowers/specs/2026-05-12-subscriptions-thread-design.md` lines 436–1825) and the distilled findings live in `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`. The key deltas from the speculative plan are summarised below — every later task that mentions endpoint names, request shape, or response field names refers to the notes file as ground truth.

### Discovery summary (load-bearing for later tasks)

- **Endpoint:** `subscriptions.thread.getView` (NOT `subscriptions.thread.list`).
- **Pagination:** form field `current_ts` set to the previous response's top-level `max_ts`; terminated by `has_more: false`. There is **no** `response_metadata.next_cursor`.
- **Response array field:** `threads` (NOT `subscriptions`). Each item is `{root_msg, latest_replies}`.
- **Subscription state lives on `root_msg`:** `root_msg.channel`, `root_msg.thread_ts`, `root_msg.last_read`, `root_msg.subscribed`.
- **`root_msg` is a complete message**, including `text`, `blocks`, `user`/`bot_id`/`bot_profile`. The plan's earlier "fetch parents for uncached threads" sub-phase is therefore unnecessary — Task 8 instead upserts `root_msg` into the `messages` table during the subscription phase.
- **New WS event `thread_subscribed`** exists alongside `thread_marked`. Same `subscription` payload shape. The plan now adds a handler for it in Task 7, plus a defensive case for the hypothetical `thread_unsubscribed`.
- **Inactive entries not observed** in the response; defensively filter on `root_msg.subscribed == true` in the adapter.

- [ ] **Step 1: Verify the notes file is present and non-empty**

```
test -s docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && echo OK
```

Expected: `OK`.

- [ ] **Step 2: Commit the notes file (already untracked in the worktree)**

```
git add docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md
git commit -m "docs: subscriptions.thread.getView endpoint discovery notes"
```

---

## Task 2: Cache migration — `thread_subscriptions` table

**Files:**
- Modify: `internal/cache/db.go` (the `schema` heredoc inside `migrate()`, currently ends just before the `CREATE INDEX` block at the bottom of the heredoc).
- Modify: `internal/cache/db_test.go` (add a new test verifying the table exists).

- [ ] **Step 1: Write the failing test**

Open `internal/cache/db_test.go` and add at the end of the file:

```go
func TestMigrate_CreatesThreadSubscriptionsTable(t *testing.T) {
	db := setupDBWithWorkspace(t)
	// PRAGMA table_info returns one row per column on an existing
	// table, zero rows if the table doesn't exist.
	rows, err := db.conn.Query("PRAGMA table_info(thread_subscriptions)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if count == 0 {
		t.Fatalf("thread_subscriptions table missing after migrate()")
	}
	const wantCols = 6 // workspace_id, channel_id, thread_ts, last_read, active, updated_at
	if count != wantCols {
		t.Fatalf("thread_subscriptions: want %d cols, got %d", wantCols, count)
	}
}
```

Note: this test accesses `db.conn` directly, the same way `setupDBWithWorkspace` builds the DB. If `db.conn` is unexported but the test is in the same package (`package cache`), the field is accessible — confirm by looking at other tests in `db_test.go`. If the package is `package cache_test`, the test must use an exported accessor — in that case adapt to whatever introspection helper already exists, or call `db.UpsertThreadSubscription` from Task 3 indirectly (defer this test to Task 3).

- [ ] **Step 2: Run test, see fail**

```
go test ./internal/cache/ -run TestMigrate_CreatesThreadSubscriptionsTable -v
```

Expected: FAIL — `thread_subscriptions table missing after migrate()`.

- [ ] **Step 3: Add the table + index to the schema heredoc**

In `internal/cache/db.go`, find the `schema := ` heredoc inside `migrate()` (currently spans ~lines 45–135). Just before the closing backtick, add:

```sql
	CREATE TABLE IF NOT EXISTS thread_subscriptions (
		workspace_id TEXT NOT NULL,
		channel_id   TEXT NOT NULL,
		thread_ts    TEXT NOT NULL,
		last_read    TEXT NOT NULL DEFAULT '',
		active       INTEGER NOT NULL DEFAULT 1,
		updated_at   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (workspace_id, channel_id, thread_ts)
	);

	CREATE INDEX IF NOT EXISTS idx_thread_subs_workspace
		ON thread_subscriptions(workspace_id, active);
```

Place the new CREATE TABLE inside the heredoc alongside the other tables, and place the new CREATE INDEX alongside the other indexes at the bottom of the heredoc. The schema is applied in one `db.conn.Exec(schema)` call so ordering inside the heredoc doesn't matter.

- [ ] **Step 4: Run test, see pass**

```
go test ./internal/cache/ -run TestMigrate_CreatesThreadSubscriptionsTable -v
```

Expected: PASS.

- [ ] **Step 5: Run all cache tests to confirm no regressions**

```
go test ./internal/cache/...
```

Expected: PASS for all packages.

- [ ] **Step 6: Commit**

```
git add internal/cache/db.go internal/cache/db_test.go
git commit -m "cache: add thread_subscriptions table"
```

---

## Task 3: `ThreadSubscription` struct + `UpsertThreadSubscription` + `DeleteThreadSubscription`

**Files:**
- Create: `internal/cache/thread_subscriptions.go`
- Create: `internal/cache/thread_subscriptions_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/cache/thread_subscriptions_test.go`:

```go
package cache

import (
	"testing"
)

func TestUpsertThreadSubscription_Insert(t *testing.T) {
	db := setupDBWithWorkspace(t)
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000000.000100", "1700000000.000200", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].ChannelID != "C1" || got[0].ThreadTS != "1700000000.000100" ||
		got[0].LastRead != "1700000000.000200" || !got[0].Active {
		t.Fatalf("row mismatch: %+v", got[0])
	}
	if got[0].UpdatedAt == 0 {
		t.Fatalf("UpdatedAt not stamped: %+v", got[0])
	}
}

func TestUpsertThreadSubscription_UpdateBumpsLastRead(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000900", true)

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 row after upsert, got %d", len(got))
	}
	if got[0].LastRead != "1700000000.000900" {
		t.Fatalf("LastRead not updated: %s", got[0].LastRead)
	}
}

func TestUpsertThreadSubscription_ToggleActive(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", false)

	got := mustList(t, db, "T1")
	if len(got) != 0 {
		t.Fatalf("inactive row should be filtered out, got %d", len(got))
	}
}

func TestUpsertThreadSubscription_PreservesLastReadAcrossReactivation(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000500", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000500", false) // tombstone
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000600", true)  // re-subscribe

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 active row after re-subscribe, got %d", len(got))
	}
	if got[0].LastRead != "1700000000.000600" {
		t.Fatalf("LastRead not updated on reactivation: %s", got[0].LastRead)
	}
}

func TestDeleteThreadSubscription_HardRemoves(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000300", "1700000000.000400", true)

	if err := db.DeleteThreadSubscription("T1", "C1", "1700000000.000100"); err != nil {
		t.Fatalf("DeleteThreadSubscription: %v", err)
	}

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 row after delete, got %d", len(got))
	}
	if got[0].ThreadTS != "1700000000.000300" {
		t.Fatalf("wrong row survived delete: %+v", got[0])
	}
}

// --- test helpers ---

func mustUpsert(t *testing.T, db *DB, ws, ch, ts, lastRead string, active bool) {
	t.Helper()
	if err := db.UpsertThreadSubscription(ws, ch, ts, lastRead, active); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
}

func mustList(t *testing.T, db *DB, ws string) []ThreadSubscription {
	t.Helper()
	got, err := db.ListActiveThreadSubscriptions(ws)
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	return got
}
```

- [ ] **Step 2: Run tests, see fail**

```
go test ./internal/cache/ -run TestUpsertThreadSubscription -v
```

Expected: FAIL — `undefined: ThreadSubscription`, `undefined: db.UpsertThreadSubscription`, etc. (compile errors).

- [ ] **Step 3: Implement struct and Upsert/Delete (plus a stub `ListActiveThreadSubscriptions` returning `nil, nil`)**

Create `internal/cache/thread_subscriptions.go`:

```go
package cache

import (
	"fmt"
	"time"
)

// ThreadSubscription is one row in the thread_subscriptions table.
// Mirrors Slack's authoritative per-thread subscription state:
// whether the user is "subscribed for unread updates" on this thread,
// and the last-read timestamp inside the thread.
type ThreadSubscription struct {
	WorkspaceID string
	ChannelID   string
	ThreadTS    string
	LastRead    string
	Active      bool
	UpdatedAt   int64 // unix seconds; bumped on every upsert
}

// UpsertThreadSubscription inserts or updates a thread_subscriptions
// row. Bumps updated_at to time.Now().Unix() on every call. Use
// active=false to tombstone a row (the row is kept so its LastRead
// survives later re-subscriptions).
func (db *DB) UpsertThreadSubscription(workspaceID, channelID, threadTS, lastRead string, active bool) error {
	if workspaceID == "" || channelID == "" || threadTS == "" {
		return fmt.Errorf("UpsertThreadSubscription: workspace/channel/thread_ts required")
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	const q = `
INSERT INTO thread_subscriptions
    (workspace_id, channel_id, thread_ts, last_read, active, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, channel_id, thread_ts) DO UPDATE SET
    last_read  = excluded.last_read,
    active     = excluded.active,
    updated_at = excluded.updated_at
`
	_, err := db.conn.Exec(q, workspaceID, channelID, threadTS, lastRead, activeInt, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("upserting thread_subscriptions: %w", err)
	}
	return nil
}

// DeleteThreadSubscription removes a thread_subscriptions row outright
// (not a tombstone). Used by tests; production callers prefer
// UpsertThreadSubscription with active=false to preserve LastRead.
func (db *DB) DeleteThreadSubscription(workspaceID, channelID, threadTS string) error {
	const q = `DELETE FROM thread_subscriptions WHERE workspace_id=? AND channel_id=? AND thread_ts=?`
	_, err := db.conn.Exec(q, workspaceID, channelID, threadTS)
	if err != nil {
		return fmt.Errorf("deleting thread_subscriptions: %w", err)
	}
	return nil
}

// ListActiveThreadSubscriptions returns every active subscription in
// the given workspace, in PRIMARY KEY order. Tombstoned rows
// (active=0) are filtered out.
func (db *DB) ListActiveThreadSubscriptions(workspaceID string) ([]ThreadSubscription, error) {
	const q = `
SELECT workspace_id, channel_id, thread_ts, last_read, active, updated_at
FROM thread_subscriptions
WHERE workspace_id = ? AND active = 1
ORDER BY channel_id, thread_ts
`
	rows, err := db.conn.Query(q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing thread_subscriptions: %w", err)
	}
	defer rows.Close()
	var out []ThreadSubscription
	for rows.Next() {
		var s ThreadSubscription
		var activeInt int
		if err := rows.Scan(&s.WorkspaceID, &s.ChannelID, &s.ThreadTS,
			&s.LastRead, &activeInt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning thread_subscriptions: %w", err)
		}
		s.Active = activeInt == 1
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests, see pass**

```
go test ./internal/cache/ -run 'TestUpsertThreadSubscription|TestDeleteThreadSubscription' -v
```

Expected: PASS for all 5 tests.

- [ ] **Step 5: Run the full cache suite**

```
go test ./internal/cache/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/cache/thread_subscriptions.go internal/cache/thread_subscriptions_test.go
git commit -m "cache: ThreadSubscription struct + Upsert/Delete/ListActive helpers"
```

---

## Task 4: `ReconcileThreadSubscriptions`

**Files:**
- Modify: `internal/cache/thread_subscriptions.go`
- Modify: `internal/cache/thread_subscriptions_test.go`

`Reconcile` is the bootstrap/reconnect-phase entry point: caller hands it the authoritative active list from Slack; it upserts each entry and tombstones any local rows missing from the fresh list (handles unsubscribes that happened while WS was disconnected).

- [ ] **Step 1: Write the failing test**

Append to `internal/cache/thread_subscriptions_test.go`:

```go
func TestReconcileThreadSubscriptions_InsertsNew(t *testing.T) {
	db := setupDBWithWorkspace(t)
	fresh := []ThreadSubscription{
		{WorkspaceID: "T1", ChannelID: "C1", ThreadTS: "1700000000.000100", LastRead: "1700000000.000200", Active: true},
		{WorkspaceID: "T1", ChannelID: "C2", ThreadTS: "1700000001.000100", LastRead: "1700000001.000200", Active: true},
	}
	if err := db.ReconcileThreadSubscriptions("T1", fresh); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := mustList(t, db, "T1")
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
}

func TestReconcileThreadSubscriptions_TombstonesMissing(t *testing.T) {
	db := setupDBWithWorkspace(t)
	// Pre-existing local row that's no longer in the fresh list.
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000500", true)
	// Fresh list contains a different thread only.
	fresh := []ThreadSubscription{
		{WorkspaceID: "T1", ChannelID: "C2", ThreadTS: "1700000001.000100", LastRead: "1700000001.000200", Active: true},
	}
	if err := db.ReconcileThreadSubscriptions("T1", fresh); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 active row after reconcile, got %d", len(got))
	}
	if got[0].ChannelID != "C2" {
		t.Fatalf("wrong row survived reconcile: %+v", got[0])
	}
	// The tombstoned row should still exist with active=0 and its LastRead preserved.
	var lastRead string
	var active int
	err := db.conn.QueryRow(
		`SELECT last_read, active FROM thread_subscriptions WHERE workspace_id=? AND channel_id=? AND thread_ts=?`,
		"T1", "C1", "1700000000.000100",
	).Scan(&lastRead, &active)
	if err != nil {
		t.Fatalf("tombstone row missing: %v", err)
	}
	if active != 0 {
		t.Fatalf("expected tombstone (active=0), got active=%d", active)
	}
	if lastRead != "1700000000.000500" {
		t.Fatalf("LastRead not preserved on tombstone: %q", lastRead)
	}
}

func TestReconcileThreadSubscriptions_UpdatesExisting(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	fresh := []ThreadSubscription{
		{WorkspaceID: "T1", ChannelID: "C1", ThreadTS: "1700000000.000100", LastRead: "1700000000.000900", Active: true},
	}
	if err := db.ReconcileThreadSubscriptions("T1", fresh); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := mustList(t, db, "T1")
	if len(got) != 1 || got[0].LastRead != "1700000000.000900" {
		t.Fatalf("Reconcile didn't update LastRead: %+v", got)
	}
}

func TestReconcileThreadSubscriptions_PerWorkspaceIsolation(t *testing.T) {
	db := setupDBWithWorkspace(t)
	// Seed both workspaces. T2 will be ignored entirely by Reconcile("T1").
	if err := db.UpsertWorkspace(Workspace{ID: "T2", Name: "T2"}); err != nil {
		t.Fatalf("UpsertWorkspace T2: %v", err)
	}
	mustUpsert(t, db, "T2", "C9", "1700000000.000100", "1700000000.000200", true)

	fresh := []ThreadSubscription{
		{WorkspaceID: "T1", ChannelID: "C1", ThreadTS: "1700000000.000100", LastRead: "1700000000.000200", Active: true},
	}
	if err := db.ReconcileThreadSubscriptions("T1", fresh); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := mustList(t, db, "T2"); len(got) != 1 {
		t.Fatalf("T2 should be unaffected, got %d active rows", len(got))
	}
}
```

- [ ] **Step 2: Run tests, see fail**

```
go test ./internal/cache/ -run TestReconcileThreadSubscriptions -v
```

Expected: FAIL — `undefined: db.ReconcileThreadSubscriptions`.

- [ ] **Step 3: Implement `ReconcileThreadSubscriptions`**

Append to `internal/cache/thread_subscriptions.go`:

```go
// ReconcileThreadSubscriptions replaces the workspace's local
// subscription set with the given fresh list. Upserts every fresh
// entry (active=1) and tombstones (active=0) any local active row
// whose (channel_id, thread_ts) doesn't appear in the fresh list.
//
// Used by the reconnect backfill: after fetching the full server-side
// list, calling this reconciles any subscribes/unsubscribes that
// happened while the WS was disconnected. Tombstoning preserves the
// row's LastRead so a later re-subscribe doesn't lose history.
func (db *DB) ReconcileThreadSubscriptions(workspaceID string, fresh []ThreadSubscription) error {
	if workspaceID == "" {
		return fmt.Errorf("ReconcileThreadSubscriptions: workspaceID required")
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin reconcile tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().Unix()

	// Build the set of keys present in the fresh list.
	type key struct{ ch, ts string }
	freshKeys := make(map[key]struct{}, len(fresh))
	for _, s := range fresh {
		freshKeys[key{s.ChannelID, s.ThreadTS}] = struct{}{}
	}

	// 1. Upsert each fresh entry as active=1.
	const upsertQ = `
INSERT INTO thread_subscriptions
    (workspace_id, channel_id, thread_ts, last_read, active, updated_at)
VALUES (?, ?, ?, ?, 1, ?)
ON CONFLICT(workspace_id, channel_id, thread_ts) DO UPDATE SET
    last_read  = excluded.last_read,
    active     = 1,
    updated_at = excluded.updated_at
`
	for _, s := range fresh {
		if _, err := tx.Exec(upsertQ, workspaceID, s.ChannelID, s.ThreadTS, s.LastRead, now); err != nil {
			return fmt.Errorf("upserting fresh subscription (%s/%s): %w", s.ChannelID, s.ThreadTS, err)
		}
	}

	// 2. Find currently-active rows that aren't in the fresh list and
	// tombstone them. Walk the existing active rows once; tombstone in
	// a second pass to avoid mutating during iteration.
	rows, err := tx.Query(
		`SELECT channel_id, thread_ts FROM thread_subscriptions WHERE workspace_id=? AND active=1`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("listing active for reconcile: %w", err)
	}
	var toTombstone []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ch, &k.ts); err != nil {
			rows.Close()
			return fmt.Errorf("scanning active for reconcile: %w", err)
		}
		if _, ok := freshKeys[k]; !ok {
			toTombstone = append(toTombstone, k)
		}
	}
	rows.Close()

	for _, k := range toTombstone {
		if _, err := tx.Exec(
			`UPDATE thread_subscriptions SET active=0, updated_at=? WHERE workspace_id=? AND channel_id=? AND thread_ts=?`,
			now, workspaceID, k.ch, k.ts,
		); err != nil {
			return fmt.Errorf("tombstoning subscription (%s/%s): %w", k.ch, k.ts, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconcile tx: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests, see pass**

```
go test ./internal/cache/ -run TestReconcileThreadSubscriptions -v
```

Expected: PASS for all four tests.

- [ ] **Step 5: Run full cache suite**

```
go test ./internal/cache/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/cache/thread_subscriptions.go internal/cache/thread_subscriptions_test.go
git commit -m "cache: ReconcileThreadSubscriptions for reconnect-time replays"
```

---

## Task 5: `ListSubscribedThreads` in `internal/cache/threads.go`

**Files:**
- Modify: `internal/cache/threads.go` (add the new function; **leave `ListInvolvedThreads` in place for now** — Task 12 deletes it after callers have switched).
- Modify: `internal/cache/threads_test.go` (new tests for the new function; keep existing `TestListInvolvedThreads_*` and `TestThreadInvolvesUser_*` tests untouched in this task).

The new SQL drives off `thread_subscriptions` (the authoritative set) instead of pivoting messages. The Go-side `ThreadSummary` struct is unchanged. `Unread` is now computed against the per-thread `last_read` from the subscription row, not the per-channel `channels.last_read_ts`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cache/threads_test.go`:

```go
// --- ListSubscribedThreads tests ---

// seedSubscribedThreadFixtures wires up two subscribed threads: A in
// channel C1 (unread — last_reply > LastRead, last reply by other),
// and B in channel C2 (read — last_reply == LastRead).
// Plus one unsubscribed-but-still-cached thread D in C1 that must
// NOT appear in the result.
func seedSubscribedThreadFixtures(t *testing.T, db *DB, selfID string) {
	t.Helper()
	if err := db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true}); err != nil {
		t.Fatalf("UpsertChannel C1: %v", err)
	}
	if err := db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T1", Name: "design", Type: "channel", IsMember: true}); err != nil {
		t.Fatalf("UpsertChannel C2: %v", err)
	}

	// Thread A in C1: parent by another user, one reply by another.
	// Subscribed; unread because last reply > LastRead and last reply by other.
	mustUpsertMsg(t, db, "1700000100.000000", "C1", "U2", "parent A", "1700000100.000000")
	mustUpsertMsg(t, db, "1700000200.000000", "C1", "U3", "reply A1", "1700000100.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription A: %v", err)
	}

	// Thread B in C2: parent by self, one reply by other.
	// Subscribed; read because LastRead == last reply.
	mustUpsertMsg(t, db, "1700000300.000000", "C2", selfID, "parent B", "1700000300.000000")
	mustUpsertMsg(t, db, "1700000400.000000", "C2", "U2", "reply B1", "1700000300.000000")
	if err := db.UpsertThreadSubscription("T1", "C2", "1700000300.000000", "1700000400.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription B: %v", err)
	}

	// Thread D in C1: parent + reply cached, but UNSUBSCRIBED.
	mustUpsertMsg(t, db, "1700000500.000000", "C1", "U2", "parent D", "1700000500.000000")
	mustUpsertMsg(t, db, "1700000600.000000", "C1", "U3", "reply D1", "1700000500.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000500.000000", "1700000500.000000", false); err != nil {
		t.Fatalf("UpsertThreadSubscription D (tombstone): %v", err)
	}
}

func mustUpsertMsg(t *testing.T, db *DB, ts, channelID, userID, text, threadTS string) {
	t.Helper()
	if err := db.UpsertMessage(Message{
		TS: ts, ChannelID: channelID, WorkspaceID: "T1", UserID: userID, Text: text, ThreadTS: threadTS,
	}); err != nil {
		t.Fatalf("UpsertMessage %s: %v", ts, err)
	}
}

func TestListSubscribedThreads_OnlySubscribedShows(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	seedSubscribedThreadFixtures(t, db, selfID)

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 subscribed threads (A and B), got %d: %+v", len(got), got)
	}
	keys := map[string]bool{}
	for _, s := range got {
		keys[s.ChannelID+":"+s.ThreadTS] = true
	}
	if !keys["C1:1700000100.000000"] || !keys["C2:1700000300.000000"] {
		t.Fatalf("missing expected threads, got keys: %v", keys)
	}
	if keys["C1:1700000500.000000"] {
		t.Fatalf("unsubscribed thread D leaked into result")
	}
}

func TestListSubscribedThreads_SortByLastReplyTSDesc(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	seedSubscribedThreadFixtures(t, db, selfID)

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want >=2, got %d", len(got))
	}
	// B has last_reply 1700000400 > A's 1700000200, so B sorts first.
	if got[0].ChannelID != "C2" {
		t.Fatalf("expected B (C2) first, got %s", got[0].ChannelID)
	}
}

func TestListSubscribedThreads_UnreadUsesPerThreadLastRead(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	// Set the channel's last_read_ts to a value AFTER the last reply —
	// the old heuristic would say "read", but the per-thread LastRead
	// from thread_subscriptions says "unread".
	if err := db.UpsertChannel(Channel{
		ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true,
		LastReadTS: "1700000999.000000",
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	mustUpsertMsg(t, db, "1700000100.000000", "C1", "U2", "parent", "1700000100.000000")
	mustUpsertMsg(t, db, "1700000200.000000", "C1", "U3", "reply", "1700000100.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if !got[0].Unread {
		t.Fatalf("expected Unread=true (per-thread LastRead=...150 < LastReplyTS=...200), got Unread=false")
	}
}

func TestListSubscribedThreads_ParentMissingShowsEmpty(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	// Subscription exists, but neither parent nor replies are cached.
	if err := db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].ParentText != "" || got[0].ParentUserID != "" {
		t.Fatalf("expected empty parent fields for uncached thread, got %+v", got[0])
	}
	// LastReplyTS falls back to the subscription's LastRead when no
	// messages are cached for the thread.
	if got[0].LastReplyTS != "1700000150.000000" {
		t.Fatalf("expected LastReplyTS to fall back to subscription LastRead, got %q", got[0].LastReplyTS)
	}
}

func TestListSubscribedThreads_PerWorkspaceIsolation(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	if err := db.UpsertWorkspace(Workspace{ID: "T2", Name: "T2"}); err != nil {
		t.Fatalf("UpsertWorkspace T2: %v", err)
	}
	if err := db.UpsertChannel(Channel{ID: "C9", WorkspaceID: "T2", Name: "other"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	if err := db.UpsertThreadSubscription("T2", "C9", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription T2: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("T1 should have 0 subscribed threads, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run tests, see fail**

```
go test ./internal/cache/ -run TestListSubscribedThreads -v
```

Expected: FAIL — `undefined: db.ListSubscribedThreads`.

- [ ] **Step 3: Implement `ListSubscribedThreads`**

Append to `internal/cache/threads.go` (do NOT remove `ListInvolvedThreads` or `ThreadInvolvesUser`):

```go
// ListSubscribedThreads returns the workspace's subscribed-threads
// list — the authoritative set from thread_subscriptions joined
// against cached message/channel data for display. Replaces the v1
// "involved threads" heuristic with Slack-side subscription state.
//
// Threads with no cached messages still appear; their parent
// text/user fall back to "" and LastReplyTS falls back to the
// subscription's LastRead so sort still produces a sensible order.
//
// Ordering: newest LastReplyTS first.
//
// Unread is computed from the subscription's per-thread LastRead
// (not the per-channel channels.last_read_ts): unread iff a reply
// exists later than LastRead AND the last reply isn't by self.
func (db *DB) ListSubscribedThreads(workspaceID, selfUserID string) ([]ThreadSummary, error) {
	const q = `
SELECT
    s.channel_id,
    s.thread_ts,
    COALESCE(c.name, ''),
    COALESCE(c.type, ''),
    s.last_read,
    COALESCE((SELECT user_id FROM messages
              WHERE channel_id = s.channel_id AND ts = s.thread_ts AND is_deleted = 0), ''),
    COALESCE((SELECT text FROM messages
              WHERE channel_id = s.channel_id AND ts = s.thread_ts AND is_deleted = 0), ''),
    (SELECT COUNT(*) FROM messages
     WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts
       AND ts != s.thread_ts AND is_deleted = 0)
        AS reply_count,
    COALESCE(
        (SELECT MAX(ts) FROM messages
         WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts AND is_deleted = 0),
        s.last_read
    ) AS last_reply_ts,
    COALESCE(
        (SELECT user_id FROM messages
         WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts AND is_deleted = 0
         ORDER BY ts DESC LIMIT 1),
        ''
    ) AS last_reply_by
FROM thread_subscriptions s
LEFT JOIN channels c ON c.id = s.channel_id
WHERE s.workspace_id = ? AND s.active = 1
`
	rows, err := db.conn.Query(q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing subscribed threads: %w", err)
	}
	defer rows.Close()

	var out []ThreadSummary
	for rows.Next() {
		var s ThreadSummary
		var lastRead string
		if err := rows.Scan(
			&s.ChannelID,
			&s.ThreadTS,
			&s.ChannelName,
			&s.ChannelType,
			&lastRead,
			&s.ParentUserID,
			&s.ParentText,
			&s.ReplyCount,
			&s.LastReplyTS,
			&s.LastReplyBy,
		); err != nil {
			return nil, fmt.Errorf("scanning subscribed thread row: %w", err)
		}
		s.ParentTS = s.ThreadTS
		s.Unread = s.LastReplyTS > lastRead && s.LastReplyBy != selfUserID && s.LastReplyBy != ""
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastReplyTS > out[j].LastReplyTS
	})
	return out, nil
}
```

- [ ] **Step 4: Run tests, see pass**

```
go test ./internal/cache/ -run TestListSubscribedThreads -v
```

Expected: PASS for all five tests.

- [ ] **Step 5: Run full cache suite (including the still-present `ListInvolvedThreads` tests)**

```
go test ./internal/cache/...
```

Expected: PASS — both old and new functions coexist; old tests still pass; new tests pass.

- [ ] **Step 6: Commit**

```
git add internal/cache/threads.go internal/cache/threads_test.go
git commit -m "cache: ListSubscribedThreads driven by thread_subscriptions"
```

---

## Task 6: `ListThreadSubscriptions` on `*slackclient.Client`

**Files:**
- Modify: `internal/slack/client.go` (add method + JSON types; reuse the `postForm` and `truncateForLog` helpers already in the file at the bottom of the package).
- Modify: `internal/slack/client_test.go` (add four tests).

**Reference Task 1's notes file** (`docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`). The concrete values for this implementation are:

- **endpoint:** `subscriptions.thread.getView`
- **pagination:** form field `current_ts` set to the previous response's top-level `max_ts`; terminated by `has_more: false`
- **request form fields:** `limit=100`, `fetch_threads_state=true`, `priority_mode=all`, plus `current_ts=<prev max_ts>` on subsequent pages
- **response array field:** `threads`
- **per-item shape:** `{root_msg: {channel, thread_ts, last_read, subscribed, user, text, ...}, latest_replies: [...]}`

The method should return both the subscription rows AND the raw `root_msg` payloads (so Task 8 can upsert them into the messages cache without a separate API call). Define a `ThreadSubscriptionView` struct that carries both, then a thin `ListThreadSubscriptions` method that returns `[]ThreadSubscriptionView`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/slack/client_test.go`:

```go
func TestListThreadSubscriptions_PaginatesUntilExhausted(t *testing.T) {
	var calls int
	var capturedCurrentTS []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		capturedCurrentTS = append(capturedCurrentTS, r.PostForm.Get("current_ts"))
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{
				"ok": true,
				"threads": [
					{"root_msg": {"channel": "C1", "ts": "1700000000.000100", "thread_ts": "1700000000.000100", "last_read": "1700000000.000200", "subscribed": true, "user": "U2", "text": "p1"}},
					{"root_msg": {"channel": "C2", "ts": "1700000001.000100", "thread_ts": "1700000001.000100", "last_read": "1700000001.000200", "subscribed": true, "user": "U3", "text": "p2"}}
				],
				"has_more": true,
				"max_ts": "1700000001.000100"
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"ok": true,
				"threads": [
					{"root_msg": {"channel": "C3", "ts": "1700000002.000100", "thread_ts": "1700000002.000100", "last_read": "1700000002.000200", "subscribed": true, "user": "U4", "text": "p3"}}
				],
				"has_more": false,
				"max_ts": "1700000002.000100"
			}`))
		default:
			t.Fatalf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()

	c := &Client{token: "xoxc-test", cookie: "d-cookie", apiBaseURL: srv.URL + "/api/"}
	got, err := c.ListThreadSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("ListThreadSubscriptions: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
	if got[0].Subscription.ChannelID != "C1" || got[2].Subscription.ChannelID != "C3" {
		t.Errorf("got = %+v", got)
	}
	if got[0].RootMessage.Text != "p1" {
		t.Errorf("expected root_msg.text to populate RootMessage.Text, got %+v", got[0].RootMessage)
	}
	if capturedCurrentTS[0] != "" || capturedCurrentTS[1] != "1700000001.000100" {
		t.Errorf("current_ts = %v, want [\"\", \"1700000001.000100\"]", capturedCurrentTS)
	}
}

func TestListThreadSubscriptions_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true, "threads": [], "has_more": false, "max_ts": ""}`))
	}))
	defer srv.Close()

	c := &Client{token: "xoxc-test", cookie: "d-cookie", apiBaseURL: srv.URL + "/api/"}
	got, err := c.ListThreadSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("ListThreadSubscriptions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestListThreadSubscriptions_RespectsHardCap(t *testing.T) {
	// Server returns 100 subs per page with has_more=true forever.
	// The client should stop after the hard cap (1000) and never make
	// an 11th call.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		var b []byte
		b = append(b, []byte(`{"ok": true, "threads": [`)...)
		for i := 0; i < 100; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, []byte(`{"root_msg": {"channel": "C", "ts": "1.0", "thread_ts": "1.0", "last_read": "1.0", "subscribed": true, "user": "U"}}`)...)
		}
		b = append(b, []byte(`], "has_more": true, "max_ts": "1.0"}`)...)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := &Client{token: "xoxc-test", cookie: "d-cookie", apiBaseURL: srv.URL + "/api/"}
	got, err := c.ListThreadSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("ListThreadSubscriptions: %v", err)
	}
	if len(got) != 1000 {
		t.Errorf("len(got) = %d, want 1000 (hard cap)", len(got))
	}
	if calls != 10 {
		t.Errorf("calls = %d, want 10 (1000 / 100 per page)", calls)
	}
}

func TestListThreadSubscriptions_FiltersUnsubscribedItems(t *testing.T) {
	// Defensively drop any items the server marks as subscribed=false.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": true,
			"threads": [
				{"root_msg": {"channel": "C1", "ts": "1.0", "thread_ts": "1.0", "last_read": "1.0", "subscribed": true}},
				{"root_msg": {"channel": "C2", "ts": "2.0", "thread_ts": "2.0", "last_read": "2.0", "subscribed": false}}
			],
			"has_more": false,
			"max_ts": ""
		}`))
	}))
	defer srv.Close()

	c := &Client{token: "xoxc-test", cookie: "d-cookie", apiBaseURL: srv.URL + "/api/"}
	got, err := c.ListThreadSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("ListThreadSubscriptions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (unsubscribed filtered out)", len(got))
	}
	if got[0].Subscription.ChannelID != "C1" {
		t.Errorf("wrong item survived filter: %+v", got[0])
	}
}

func TestListThreadSubscriptions_ReturnsErrorOnNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "invalid_auth"}`))
	}))
	defer srv.Close()

	c := &Client{token: "xoxc-test", cookie: "d-cookie", apiBaseURL: srv.URL + "/api/"}
	_, err := c.ListThreadSubscriptions(context.Background())
	if err == nil {
		t.Fatalf("expected error on ok=false, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("error = %q, want contains \"invalid_auth\"", err.Error())
	}
}
```

(If `strings` isn't already imported in `client_test.go`, the existing test file already does — confirm and add if missing.)

- [ ] **Step 2: Run tests, see fail**

```
go test ./internal/slack/ -run TestListThreadSubscriptions -v
```

Expected: FAIL — `undefined: c.ListThreadSubscriptions`.

- [ ] **Step 3: Implement `ListThreadSubscriptions`**

Append to `internal/slack/client.go`. Place near the other hand-rolled paginated endpoints (e.g. just below `GetChannelSections` / `callChannelSectionsList`). The method returns `[]ThreadSubscriptionView` where each view bundles the parsed `Subscription` row with the raw `root_msg` payload — the caller (Task 8) needs both: `Subscription` feeds `ReconcileThreadSubscriptions`, and `RootMessage` lets us pre-cache the parent without a separate `conversations.replies` call.

```go
// ThreadSubscription is the slk-side projection of one subscribed
// thread returned by subscriptions.thread.getView. The five fields
// here map cleanly onto cache.ThreadSubscription. The caller in
// cmd/slk/reconnect_backfill.go does the adapter cast.
type ThreadSubscription struct {
	ChannelID string
	ThreadTS  string
	LastRead  string
	Active    bool
}

// ThreadSubscriptionView is one item from subscriptions.thread.getView.
// It carries both the subscription-state projection (Subscription) and
// the full parent message Slack ships inside root_msg
// (RootMessage). The subscription-phase backfiller uses Subscription
// to upsert the thread_subscriptions row and RootMessage to upsert
// the parent into the messages cache, eliminating the need for a
// follow-up conversations.replies fetch when the parent isn't
// already cached.
type ThreadSubscriptionView struct {
	Subscription ThreadSubscription
	RootMessage  slack.Message
}

// listThreadSubscriptionsResponse decodes one page of
// subscriptions.thread.getView. The wire shape is:
//
//	{
//	  "ok": true,
//	  "threads": [
//	    {"root_msg": {channel, ts, thread_ts, last_read, subscribed, ...}, "latest_replies": [...]},
//	    ...
//	  ],
//	  "has_more": true,
//	  "max_ts": "1700000001.000100"
//	}
type listThreadSubscriptionsResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Threads []struct {
		// RootMsg is decoded twice: once into the typed
		// slackThreadRootMsg shape for the channel/last_read/subscribed
		// fields we need, and once into a slack.Message via json.RawMessage
		// so the caller can re-marshal it for the messages cache.
		RootMsg json.RawMessage `json:"root_msg"`
	} `json:"threads"`
	HasMore bool   `json:"has_more"`
	MaxTS   string `json:"max_ts"`
}

// slackThreadRootMsg is the subset of root_msg fields the
// subscription-phase reconcile needs. The rest of root_msg flows
// through as slack.Message via the raw JSON re-parse.
type slackThreadRootMsg struct {
	Channel    string `json:"channel"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	LastRead   string `json:"last_read"`
	Subscribed bool   `json:"subscribed"`
}

// listThreadSubscriptionsHardCap bounds how many subscriptions
// ListThreadSubscriptions will return per call. Protects against
// runaway requests if Slack ships a buggy has_more flag.
const listThreadSubscriptionsHardCap = 1000

// ListThreadSubscriptions fetches the workspace's full subscribed-
// threads list via Slack's internal subscriptions.thread.getView
// endpoint (the same call the official web client makes when
// bootstrapping its Threads view). Paginates via the `current_ts`
// form field (set to the previous response's max_ts), terminated by
// has_more=false. Stops at listThreadSubscriptionsHardCap items.
//
// Items where root_msg.subscribed is false are filtered out —
// defensive, since the live endpoint hasn't been observed returning
// them.
//
// Returns (nil, err) on network failure or ok=false JSON. The caller
// (the reconnect backfill phase) treats any error as "subscriptions
// unavailable" and surfaces the UI banner.
func (c *Client) ListThreadSubscriptions(ctx context.Context) ([]ThreadSubscriptionView, error) {
	var all []ThreadSubscriptionView
	currentTS := ""
	for {
		body, err := c.callListThreadSubscriptions(ctx, currentTS)
		if err != nil {
			return nil, err
		}
		var resp listThreadSubscriptionsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing subscriptions.thread.getView: %w (body=%s)", err, truncateForLog(body))
		}
		if !resp.OK {
			return nil, fmt.Errorf("subscriptions.thread.getView: %s (body=%s)", resp.Error, truncateForLog(body))
		}
		for _, item := range resp.Threads {
			var sm slackThreadRootMsg
			if err := json.Unmarshal(item.RootMsg, &sm); err != nil {
				// Skip malformed items but keep paginating.
				debuglog.Backfill("ListThreadSubscriptions: skipping malformed root_msg: %v", err)
				continue
			}
			if !sm.Subscribed {
				continue
			}
			var raw slack.Message
			if err := json.Unmarshal(item.RootMsg, &raw); err != nil {
				// Couldn't decode the rich message; skip so we don't
				// corrupt the messages cache, but the subscription row
				// is still useful — fall back to a synthetic empty
				// slack.Message so the caller can still record the row.
				debuglog.Backfill("ListThreadSubscriptions: root_msg slack.Message decode err=%v; subscription kept without RootMessage", err)
				raw = slack.Message{}
			}
			all = append(all, ThreadSubscriptionView{
				Subscription: ThreadSubscription{
					ChannelID: sm.Channel,
					ThreadTS:  sm.ThreadTS,
					LastRead:  sm.LastRead,
					Active:    sm.Subscribed,
				},
				RootMessage: raw,
			})
			if len(all) >= listThreadSubscriptionsHardCap {
				debuglog.Backfill("ListThreadSubscriptions: hit hard cap %d, stopping", listThreadSubscriptionsHardCap)
				return all, nil
			}
		}
		if !resp.HasMore || resp.MaxTS == "" || resp.MaxTS == currentTS {
			break
		}
		currentTS = resp.MaxTS
	}
	return all, nil
}

func (c *Client) callListThreadSubscriptions(ctx context.Context, currentTS string) ([]byte, error) {
	form := url.Values{}
	form.Set("limit", "100")
	form.Set("fetch_threads_state", "true")
	form.Set("priority_mode", "all")
	if currentTS != "" {
		form.Set("current_ts", currentTS)
	}
	return c.postForm(ctx, "subscriptions.thread.getView", form)
}
```

Imports to add to `internal/slack/client.go` if not already present:
- `"github.com/gammons/slk/internal/debuglog"`

Check with:

```
grep '"github.com/gammons/slk/internal/debuglog"' internal/slack/client.go
```

- [ ] **Step 4: Run tests, see pass**

```
go test ./internal/slack/ -run TestListThreadSubscriptions -v
```

Expected: PASS for all five tests.

- [ ] **Step 5: Run full slack-package suite**

```
go test ./internal/slack/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/slack/client.go internal/slack/client_test.go
git commit -m "slack: ListThreadSubscriptions via subscriptions.thread.getView"
```

---

## Task 7: Extend WS event handlers to persist subscription state

**Files:**
- Modify: `internal/slack/events.go` (parse `thread_subscribed` and `thread_unsubscribed` events; add `OnThreadSubscriptionChanged` to the `EventHandler` interface; dispatch the events).
- Modify: `cmd/slk/main.go` (extend `OnThreadMarked` to upsert; implement `OnThreadSubscriptionChanged`).
- Modify: `cmd/slk/event_handler_test.go` (extends existing `OnConversationOpened` / `OnMessage` test patterns; `package main`; uses the `newTestDB` helper from `reconnect_backfill_test.go`).

Per the discovery notes, Slack ships **two** WS events that mutate per-thread subscription state:

1. `thread_marked` (already parsed; payload `subscription{channel,thread_ts,last_read,active}`) — fires on read/unread toggle.
2. `thread_subscribed` (new; same `subscription{...}` payload shape) — fires when the user newly subscribes to a thread (via reply, mention, manual subscribe).
3. `thread_unsubscribed` (hypothetical mirror of #2; not observed yet but plausibly exists) — handle defensively.

Both events should call the same handler logic: upsert a `thread_subscriptions` row reflecting the new state. The existing `OnThreadMarked` handler stays focused on read-state UI side effects.

- [ ] **Step 1: Find the existing `OnThreadMarked` test, if any**

```
grep -rn "OnThreadMarked" cmd/slk/
```

If none, this task creates `TestOnThreadMarked_UpsertsSubscription` and `TestOnThreadSubscriptionChanged_UpsertsSubscription`.

- [ ] **Step 2: Write the failing tests**

Add to `cmd/slk/event_handler_test.go`:

```go
func TestOnThreadMarked_UpsertsSubscription(t *testing.T) {
	db := newTestDB(t) // helper from reconnect_backfill_test.go (package main)
	h := &rtmEventHandler{
		db:          db,
		workspaceID: "T1",
		isActive:    func() bool { return true },
		// program/notifier left nil; the handler nil-checks before
		// dispatching the UI message.
	}

	// read=false means the thread is now unread, which corresponds to
	// active=true in thread_subscriptions.
	h.OnThreadMarked("C1", "1700000100.000000", "1700000150.000000", false)

	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 active sub after thread_marked, got %d", len(got))
	}
	if got[0].ChannelID != "C1" || got[0].ThreadTS != "1700000100.000000" ||
		got[0].LastRead != "1700000150.000000" || !got[0].Active {
		t.Fatalf("subscription row mismatch: %+v", got[0])
	}

	// Marking read flips the row to inactive (tombstone-style).
	h.OnThreadMarked("C1", "1700000100.000000", "1700000150.000000", true)
	got, _ = db.ListActiveThreadSubscriptions("T1")
	if len(got) != 0 {
		t.Fatalf("expected 0 active after read=true, got %d", len(got))
	}
}

func TestOnThreadSubscriptionChanged_UpsertsActive(t *testing.T) {
	db := newTestDB(t)
	h := &rtmEventHandler{
		db:          db,
		workspaceID: "T1",
		isActive:    func() bool { return true },
	}

	// A fresh thread_subscribed event with active=true.
	h.OnThreadSubscriptionChanged("C1", "1700000100.000000", "1700000150.000000", true)

	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != "C1" || !got[0].Active {
		t.Fatalf("subscription row mismatch: %+v", got)
	}
}

func TestOnThreadSubscriptionChanged_TombstonesOnUnsubscribe(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
	h := &rtmEventHandler{
		db:          db,
		workspaceID: "T1",
		isActive:    func() bool { return true },
	}

	// Defensive: simulate the hypothetical thread_unsubscribed event by
	// calling the handler with active=false.
	h.OnThreadSubscriptionChanged("C1", "1700000100.000000", "1700000150.000000", false)

	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 active after unsubscribe, got %d: %+v", len(got), got)
	}
}
```

- [ ] **Step 3: Run tests, see fail**

```
go test ./cmd/slk/ -run 'TestOnThreadMarked_UpsertsSubscription|TestOnThreadSubscriptionChanged' -v
```

Expected: FAIL — missing methods on `rtmEventHandler`.

- [ ] **Step 4: Add the new EventHandler method**

In `internal/slack/events.go`, extend the `EventHandler` interface. Add this method alongside `OnThreadMarked`:

```go
// OnThreadSubscriptionChanged is delivered for thread_subscribed and
// thread_unsubscribed WS events. active=true on subscribe,
// active=false on unsubscribe. lastRead is the per-thread last_read ts
// the server reports — pass-through to thread_subscriptions.last_read.
// The payload shape is identical to thread_marked.subscription, so
// implementations can share state-update logic with OnThreadMarked
// (this handler is the persistence-only path; OnThreadMarked also
// drives the UI's read-state side effects).
OnThreadSubscriptionChanged(channelID, threadTS, lastRead string, active bool)
```

In `internal/slack/events.go`, add a parsing struct (right next to `wsThreadMarkedEvent`):

```go
// wsThreadSubscribedEvent represents thread_subscribed and
// thread_unsubscribed events from Slack's browser-protocol
// WebSocket. The subscription block has the same shape as
// wsThreadMarkedEvent.subscription.
type wsThreadSubscribedEvent struct {
	Type         string `json:"type"`
	Subscription struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		LastRead string `json:"last_read"`
		Active   bool   `json:"active"`
	} `json:"subscription"`
}
```

In `dispatchWebSocketEvent`, add cases right after the `thread_marked` case:

```go
case "thread_subscribed", "thread_unsubscribed":
	var evt wsThreadSubscribedEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return
	}
	// thread_unsubscribed events should be treated as active=false
	// regardless of what the server marks the inner flag as; the
	// outer event type is authoritative.
	active := evt.Subscription.Active
	if eventType == "thread_unsubscribed" {
		active = false
	}
	debuglog.WS("%s: channel=%s thread_ts=%s last_read=%s active=%v",
		eventType, evt.Subscription.Channel, evt.Subscription.ThreadTS, evt.Subscription.LastRead, active)
	handler.OnThreadSubscriptionChanged(
		evt.Subscription.Channel,
		evt.Subscription.ThreadTS,
		evt.Subscription.LastRead,
		active,
	)
```

(Confirm the variable name for the dispatched event type in the existing `switch` — it's the value being switched on; `eventType` here is illustrative.)

- [ ] **Step 5: Update all `EventHandler` implementations**

The compile error after adding the interface method will surface every implementation that needs an `OnThreadSubscriptionChanged`. The main one lives on `rtmEventHandler` in `cmd/slk/main.go`. Any test fakes for `EventHandler` (e.g. mock handlers in `internal/slack/*_test.go`) also need the no-op method.

Add to `cmd/slk/main.go` (near `OnThreadMarked`):

```go
// OnThreadSubscriptionChanged persists a subscribe/unsubscribe event
// in the thread_subscriptions table. The threads-view UI refresh is
// handled by the same ThreadsListDirtyMsg dispatch path the rest of
// the reconnect / mark events use — no per-event UI message is
// emitted here, because the threads-view rerender is driven off the
// cache content rather than off direct event injection.
func (h *rtmEventHandler) OnThreadSubscriptionChanged(channelID, threadTS, lastRead string, active bool) {
	if h.isActive != nil && !h.isActive() {
		return
	}
	if h.db != nil {
		if err := h.db.UpsertThreadSubscription(h.workspaceID, channelID, threadTS, lastRead, active); err != nil {
			debuglog.Cache("OnThreadSubscriptionChanged: UpsertThreadSubscription %s/%s: %v",
				channelID, threadTS, err)
		}
	}
	if h.program != nil {
		// Trigger a threads-view refetch so the new row shows up
		// (active=true) or the existing row disappears (active=false).
		h.program.Send(ui.ThreadsListDirtyMsg{TeamID: h.workspaceID})
	}
}
```

And replace the body of `OnThreadMarked`:

```go
func (h *rtmEventHandler) OnThreadMarked(channelID, threadTS, ts string, read bool) {
	if h.isActive != nil && !h.isActive() {
		return
	}

	// Persist subscription state. active = !read per the dispatch in
	// internal/slack/events.go: WS `active` means "subscribed for
	// unread updates", which corresponds to active=1 in our table.
	if h.db != nil {
		if err := h.db.UpsertThreadSubscription(h.workspaceID, channelID, threadTS, ts, !read); err != nil {
			debuglog.Cache("OnThreadMarked: UpsertThreadSubscription %s/%s: %v",
				channelID, threadTS, err)
		}
	}

	if h.program == nil {
		return
	}
	h.program.Send(ui.ThreadMarkedRemoteMsg{
		ChannelID: channelID,
		ThreadTS:  threadTS,
		TS:        ts,
		Read:      read,
	})
}
```

If `debuglog` isn't imported in `cmd/slk/main.go`, add the import — most of the file already uses it, so the import should already be there. If any `EventHandler` test fakes in `internal/slack/` complain, give them empty `OnThreadSubscriptionChanged` method receivers.

- [ ] **Step 6: Run tests, see pass**

```
go test ./internal/slack/... ./cmd/slk/... -run 'TestOnThreadMarked_UpsertsSubscription|TestOnThreadSubscriptionChanged' -v
```

Expected: PASS for all three tests.

- [ ] **Step 7: Run full suites to catch missed handler impls**

```
go test ./...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```
git add internal/slack/events.go cmd/slk/main.go cmd/slk/event_handler_test.go
git commit -m "ws: thread_subscribed / thread_marked persist to thread_subscriptions"
```

---

## Task 8: Backfiller `runSubscriptionPhase`

**Files:**
- Modify: `cmd/slk/reconnect_backfill.go` (extend `historyFetcher` interface; add `runSubscriptionPhase`; thread an `availableCb` into `backfiller` so failure can flip `wctx.SubscriptionsAvailable` without `cmd/slk/reconnect_backfill.go` reaching into the `WorkspaceContext` type).
- Modify: `cmd/slk/reconnect_backfill_test.go` (extend `fakeHistory` with `ListThreadSubscriptions`; add new tests).

`*slackclient.Client` already implicitly satisfies the new interface method after Task 6 lands.

**Key simplification from discovery:** the `subscriptions.thread.getView` response already carries the full parent message in `root_msg` (text, blocks, user, channel, etc.). The plan no longer needs a separate "fetch parents for uncached threads via GetReplies" sub-phase — `runSubscriptionPhase` upserts each `RootMessage` directly into the `messages` cache. This eliminates a per-thread round trip and means `db.HasMessage` from the earlier plan draft is unnecessary.

- [ ] **Step 1: Extend `historyFetcher` and `fakeHistory` first (no behavior change)**

In `cmd/slk/reconnect_backfill.go`, change:

```go
type historyFetcher interface {
	GetHistorySince(ctx context.Context, channelID, oldest string, maxTotal int) ([]slack.Message, error)
	GetReplies(ctx context.Context, channelID, threadTS string) ([]slack.Message, error)
}
```

to:

```go
type historyFetcher interface {
	GetHistorySince(ctx context.Context, channelID, oldest string, maxTotal int) ([]slack.Message, error)
	GetReplies(ctx context.Context, channelID, threadTS string) ([]slack.Message, error)
	ListThreadSubscriptions(ctx context.Context) ([]slackclient.ThreadSubscriptionView, error)
}
```

Add the import:

```go
import (
	...
	slackclient "github.com/gammons/slk/internal/slack"
	...
)
```

(Use the alias `slackclient` to match the convention used elsewhere in `cmd/slk`.)

In `cmd/slk/reconnect_backfill_test.go`, extend `fakeHistory`:

```go
type fakeHistory struct {
	// ... existing fields ...
	subscriptionsResponse []slackclient.ThreadSubscriptionView
	subscriptionsErr      error
	subscriptionsCalls    int
}

func (f *fakeHistory) ListThreadSubscriptions(ctx context.Context) ([]slackclient.ThreadSubscriptionView, error) {
	f.mu.Lock()
	f.subscriptionsCalls++
	f.mu.Unlock()
	if f.subscriptionsErr != nil {
		return nil, f.subscriptionsErr
	}
	return f.subscriptionsResponse, nil
}
```

Add the `slackclient` import to `reconnect_backfill_test.go` too. Run the existing test suite to confirm the change compiles:

```
go test ./cmd/slk/... -run TestBackfill -count=1
```

Expected: PASS — no behavior change yet, just the interface widening.

- [ ] **Step 2: Add the availability callback to `backfiller`**

In `cmd/slk/reconnect_backfill.go`, extend the struct:

```go
type backfiller struct {
	// ... existing fields ...

	// availableCb, if non-nil, is called with the outcome of the
	// subscription-phase API call: true on success, false on error.
	// The OnConnect site wires this to wctx.SubscriptionsAvailable so
	// the UI banner reflects the most recent attempt.
	availableCb func(bool)
}
```

Adjust `newBackfiller` to accept the callback:

```go
func newBackfiller(client historyFetcher, db *cache.DB, workspaceID, selfUserID string, program teaSender, concurrency, perChannelCap int, availableCb func(bool)) *backfiller {
	if concurrency < 1 {
		concurrency = 1
	}
	if perChannelCap < 1 {
		perChannelCap = 500
	}
	return &backfiller{
		client:            client,
		db:                db,
		workspaceID:       workspaceID,
		selfUserID:        selfUserID,
		program:           program,
		concurrency:       concurrency,
		perChannelCap:     perChannelCap,
		discoveredThreads: map[threadKey]struct{}{},
		availableCb:       availableCb,
	}
}
```

Update the call site at `cmd/slk/main.go:2705-2718` (inside `OnConnect`):

```go
bf := newBackfiller(
	wctx.Client, db, workspaceID, wctx.Client.UserID(), program, 4, 500,
	func(available bool) {
		wctx.SubscriptionsAvailable = available
	},
)
```

(`wctx.SubscriptionsAvailable` is added in Task 9 — for now this won't compile until Task 9 is also done. Defer the call-site change to Task 9; Step 2 of this task ONLY adds the `availableCb` field and the constructor parameter on the `cmd/slk/reconnect_backfill.go` side, leaving the call site untouched. Update the existing call site to pass `nil` as the new last argument so the build stays green:)

In `cmd/slk/main.go`, change the existing `newBackfiller(wctx.Client, db, workspaceID, wctx.Client.UserID(), program, 4, 500)` call to:

```go
bf := newBackfiller(wctx.Client, db, workspaceID, wctx.Client.UserID(), program, 4, 500, nil /* availableCb wired in Task 9 */)
```

Run the build:

```
go build ./...
```

Expected: clean.

Run existing tests:

```
go test ./cmd/slk/... -run TestBackfill -count=1
```

Expected: PASS — these tests should pass a `nil` availableCb too (update the test invocations of `newBackfiller` similarly).

- [ ] **Step 3: Write the failing tests for `runSubscriptionPhase`**

Append to `cmd/slk/reconnect_backfill_test.go`. Helper `subView` constructs a `ThreadSubscriptionView` from primitives so the tests stay terse:

```go
func subView(channel, threadTS, lastRead, text, user string, active bool) slackclient.ThreadSubscriptionView {
	return slackclient.ThreadSubscriptionView{
		Subscription: slackclient.ThreadSubscription{
			ChannelID: channel, ThreadTS: threadTS, LastRead: lastRead, Active: active,
		},
		RootMessage: slack.Message{
			Msg: slack.Msg{
				Timestamp:       threadTS,
				ThreadTimestamp: threadTS,
				User:            user,
				Text:            text,
				Channel:         channel,
			},
		},
	}
}

func TestBackfillSubscriptions_PopulatesTable(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{}, // no channels
		subscriptionsResponse: []slackclient.ThreadSubscriptionView{
			subView("C1", "1700000100.000000", "1700000150.000000", "p1", "U2", true),
			subView("C2", "1700000200.000000", "1700000250.000000", "p2", "U3", true),
		},
	}
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, nil)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}
	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 subscriptions in DB, got %d", len(got))
	}
}

func TestBackfillSubscriptions_UpsertsRootMessageIntoMessagesCache(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	fake := &fakeHistory{
		subscriptionsResponse: []slackclient.ThreadSubscriptionView{
			subView("C1", "1700000100.000000", "1700000150.000000", "parent X", "U2", true),
		},
	}
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, nil)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}

	// The root_msg should have been upserted into the messages cache.
	msgs, err := db.GetThreadReplies("C1", "1700000100.000000")
	if err != nil {
		t.Fatalf("GetThreadReplies: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 cached message (the parent), got %d", len(msgs))
	}
	if msgs[0].Text != "parent X" || msgs[0].UserID != "U2" {
		t.Fatalf("root_msg fields not preserved: %+v", msgs[0])
	}

	// No GetReplies calls should have been made — root_msg already
	// gave us the parent.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.repliesCalls) != 0 {
		t.Fatalf("expected 0 GetReplies calls, got %d: %v", len(fake.repliesCalls), fake.repliesCalls)
	}
}

func TestBackfillSubscriptions_ReconcilesUnsubscribes(t *testing.T) {
	db := newTestDB(t)
	// Seed a local subscription that's no longer in the server's fresh list.
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
	fake := &fakeHistory{
		subscriptionsResponse: []slackclient.ThreadSubscriptionView{
			subView("C2", "1700000300.000000", "1700000350.000000", "p2", "U3", true),
		},
	}
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, nil)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}
	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != "C2" {
		t.Fatalf("expected only C2 active after reconcile, got %+v", got)
	}
}

func TestBackfillSubscriptions_ErrorTriggersAvailabilityCallback(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeHistory{
		subscriptionsErr: errors.New("network kaboom"),
	}
	var calls []bool
	cb := func(available bool) { calls = append(calls, available) }
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, cb)

	if err := bf.runSubscriptionPhase(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(calls) != 1 || calls[0] != false {
		t.Fatalf("expected one callback with available=false, got %v", calls)
	}
}

func TestBackfillSubscriptions_SuccessTriggersAvailabilityCallback(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeHistory{}
	var calls []bool
	cb := func(available bool) { calls = append(calls, available) }
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, cb)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}
	if len(calls) != 1 || calls[0] != true {
		t.Fatalf("expected one callback with available=true, got %v", calls)
	}
}
```

You'll need `"errors"` imported in `reconnect_backfill_test.go`; the existing tests likely already import it.

- [ ] **Step 4: Run tests, see fail**

```
go test ./cmd/slk/ -run TestBackfillSubscriptions -v
```

Expected: FAIL — `runSubscriptionPhase` undefined.

- [ ] **Step 5: Implement `runSubscriptionPhase`**

Append to `cmd/slk/reconnect_backfill.go`:

```go
// runSubscriptionPhase fetches the workspace's full thread-subscription
// list via subscriptions.thread.getView, reconciles the local
// thread_subscriptions table against it, and upserts every returned
// root_msg into the messages cache so the threads-view can render
// parents even for threads slk has never seen a message from.
//
// Side effects:
//  1. thread_subscriptions reflects the server's authoritative state.
//  2. b.availableCb is called with true/false so the UI banner can
//     reflect the outcome.
//  3. Each ThreadSubscriptionView.RootMessage is upserted into the
//     messages cache (idempotent by (ts, channel_id)).
//
// Errors from the API call are returned to the caller; per-thread
// message-upsert failures are logged and skipped (one bad message
// does not abort the pass).
func (b *backfiller) runSubscriptionPhase(ctx context.Context) error {
	start := time.Now()
	views, err := b.client.ListThreadSubscriptions(ctx)
	if err != nil {
		debuglog.Backfill("team=%s subscription-phase err=%v", b.workspaceID, err)
		if b.availableCb != nil {
			b.availableCb(false)
		}
		return err
	}
	if b.availableCb != nil {
		b.availableCb(true)
	}

	// Adapt slack-client view rows into cache.ThreadSubscription. The
	// API method already filters out subscribed=false items, so the
	// list is conservative: every item here is currently active.
	fresh := make([]cache.ThreadSubscription, 0, len(views))
	for _, v := range views {
		if !v.Subscription.Active {
			continue
		}
		fresh = append(fresh, cache.ThreadSubscription{
			WorkspaceID: b.workspaceID,
			ChannelID:   v.Subscription.ChannelID,
			ThreadTS:    v.Subscription.ThreadTS,
			LastRead:    v.Subscription.LastRead,
			Active:      true,
		})
	}
	if err := b.db.ReconcileThreadSubscriptions(b.workspaceID, fresh); err != nil {
		debuglog.Backfill("team=%s reconcile err=%v", b.workspaceID, err)
		return err
	}

	// Upsert the root_msg from every view into the messages cache.
	// Mirrors the upsert pattern in backfillOneThread (the parent
	// payload comes from a different endpoint here, but the cache
	// row shape is the same). Skip entries where RootMessage is
	// empty (Subscription kept but RootMessage couldn't be decoded;
	// see the ListThreadSubscriptions docstring).
	upserted := 0
	for _, v := range views {
		if v.RootMessage.Timestamp == "" {
			continue
		}
		raw, _ := json.Marshal(v.RootMessage)
		if err := b.db.UpsertMessage(cache.Message{
			TS:          v.RootMessage.Timestamp,
			ChannelID:   v.Subscription.ChannelID,
			WorkspaceID: b.workspaceID,
			UserID:      v.RootMessage.User,
			Text:        v.RootMessage.Text,
			ThreadTS:    v.RootMessage.ThreadTimestamp,
			ReplyCount:  v.RootMessage.ReplyCount,
			Subtype:     v.RootMessage.SubType,
			RawJSON:     string(raw),
			CreatedAt:   time.Now().Unix(),
		}); err != nil {
			debuglog.Backfill("team=%s subscription-phase upsert root_msg %s/%s err=%v",
				b.workspaceID, v.Subscription.ChannelID, v.Subscription.ThreadTS, err)
			continue
		}
		upserted++
	}

	debuglog.Backfill("team=%s subscription-phase subs=%d root_msgs_upserted=%d dur_ms=%d",
		b.workspaceID, len(fresh), upserted, time.Since(start).Milliseconds())
	return nil
}
```

Hook the new phase into `run()` so it runs between thread-phase and the `ThreadsListDirtyMsg` dispatch. Update:

```go
func (b *backfiller) run(ctx context.Context) error {
	start := time.Now()
	if err := b.runChannelPhase(ctx); err != nil {
		debuglog.Backfill("team=%s channel-phase err=%v", b.workspaceID, err)
	}
	if err := b.runThreadPhase(ctx); err != nil {
		debuglog.Backfill("team=%s thread-phase err=%v", b.workspaceID, err)
	}
	if err := b.runSubscriptionPhase(ctx); err != nil {
		debuglog.Backfill("team=%s subscription-phase err=%v", b.workspaceID, err)
	}
	if b.program != nil {
		b.program.Send(ui.ThreadsListDirtyMsg{TeamID: b.workspaceID})
	}
	debuglog.Backfill("team=%s trigger=reconnect total_dur_ms=%d status=ok",
		b.workspaceID, time.Since(start).Milliseconds())
	return nil
}
```

- [ ] **Step 6: Run tests, see pass**

```
go test ./cmd/slk/ -run TestBackfillSubscriptions -v
```

Expected: PASS for all six tests.

- [ ] **Step 7: Run the full cmd/slk + cache suites**

```
go test ./internal/cache/... ./cmd/slk/...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```
git add cmd/slk/reconnect_backfill.go cmd/slk/reconnect_backfill_test.go cmd/slk/main.go
git commit -m "backfill: subscription phase via subscriptions.thread.getView"
```

---

## Task 9: `WorkspaceContext.SubscriptionsAvailable` + `ThreadsListLoadedMsg` field + closure swap

**Files:**
- Modify: `cmd/slk/main.go` (add field to `WorkspaceContext`; initialise to true; wire the `availableCb` argument to `newBackfiller`; swap the `SetThreadsListFetcher` closure to call `ListSubscribedThreads` and stamp the flag).
- Modify: `internal/ui/app.go` (add `SubscriptionsAvailable bool` field to `ThreadsListLoadedMsg` and forward it into the threads view via a new setter).

This task does NOT touch the threadsview model — that's Task 10. After this task the wiring exists end-to-end but the banner isn't rendered yet.

- [ ] **Step 1: Add the field to `WorkspaceContext`**

In `cmd/slk/main.go`, find the `WorkspaceContext` struct (lines 92-159 today) and add immediately after `ThreadsHasUnreads`:

```go
	// SubscriptionsAvailable indicates whether the most recent
	// runSubscriptionPhase attempt succeeded in fetching Slack's
	// authoritative thread-subscription list. true on bootstrap
	// (optimistic — no banner during the brief pre-bootstrap
	// window) and after every successful subscription phase; false
	// after a failed one. The UI uses it to decide whether to draw
	// the "Threads list unavailable" banner.
	SubscriptionsAvailable bool
```

Find the `WorkspaceContext` construction site (search for `WorkspaceContext{` in `cmd/slk/main.go`) and add the initial value:

```go
wctx := &WorkspaceContext{
	...
	SubscriptionsAvailable: true,
	...
}
```

If construction is field-by-field after the struct literal (it is — `wctx.TeamID = ...` style), add a single `wctx.SubscriptionsAvailable = true` line in the same block.

- [ ] **Step 2: Wire the availability callback**

Update the existing `newBackfiller` call site in `OnConnect` (replace the `nil` placeholder added in Task 8):

```go
bf := newBackfiller(
	wctx.Client, db, workspaceID, wctx.Client.UserID(), program, 4, 500,
	func(available bool) { wctx.SubscriptionsAvailable = available },
)
```

Build:

```
go build ./...
```

Expected: clean.

- [ ] **Step 3: Add the field to `ThreadsListLoadedMsg`**

In `internal/ui/app.go`, find the `ThreadsListLoadedMsg` struct (line 168) and add:

```go
ThreadsListLoadedMsg struct {
	TeamID                 string
	Summaries              []cache.ThreadSummary
	// SubscriptionsAvailable reflects whether the most recent
	// runSubscriptionPhase succeeded in fetching the authoritative
	// thread-subscription list. The threads view renders a banner
	// when false (Task 10 wires the renderer).
	SubscriptionsAvailable bool
}
```

In the `ThreadsListLoadedMsg` handler at `internal/ui/app.go:1936-1948`, add a call to a new setter on `threadsView` (the implementation of the setter ships in Task 10; for this task we only add the call site so the handler is final):

```go
case ThreadsListLoadedMsg:
	if msg.TeamID == a.activeTeamID {
		a.threadsView.SetSummaries(msg.Summaries)
		a.threadsView.SetSubscriptionsAvailable(msg.SubscriptionsAvailable)
		a.sidebar.SetThreadsUnreadCount(a.threadsView.UnreadCount())
		if a.view == ViewThreads {
			if cmd := a.openSelectedThreadCmd(false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
```

This will not compile yet (`SetSubscriptionsAvailable` doesn't exist on `threadsview.Model`). To keep this task's commit green, add a temporary no-op setter to `internal/ui/threadsview/model.go` right next to the other setters:

```go
// SetSubscriptionsAvailable records whether Slack's
// subscription-state API is currently reachable. The banner the View
// renders when false is wired in the next task; for now this is a
// no-op stub so callers compile.
func (m *Model) SetSubscriptionsAvailable(available bool) {
	// no-op (banner rendering implemented in Task 10)
}
```

Task 10 replaces the stub.

- [ ] **Step 4: Swap the `SetThreadsListFetcher` closure to call `ListSubscribedThreads` and forward the flag**

In `cmd/slk/main.go:1018-1043`, replace the existing closure body:

```go
app.SetThreadsListFetcher(func(teamID string) tea.Msg {
	wctx := router.Active()
	if wctx == nil {
		return nil
	}
	summaries, err := db.ListSubscribedThreads(teamID, wctx.Client.UserID())
	if err != nil {
		log.Printf("Warning: ListSubscribedThreads(%s): %v", teamID, err)
		return ui.ThreadsListLoadedMsg{
			TeamID:                 teamID,
			Summaries:              nil,
			SubscriptionsAvailable: wctx.SubscriptionsAvailable,
		}
	}
	// With per-thread last_read in thread_subscriptions, the Unread
	// flag is now authoritative — the old ThreadsHasUnreads
	// suppression heuristic that protected against stale
	// channels.last_read_ts is no longer needed. The closure that
	// previously zeroed all Unread flags when wctx.ThreadsHasUnreads
	// was false has been removed.
	return ui.ThreadsListLoadedMsg{
		TeamID:                 teamID,
		Summaries:              summaries,
		SubscriptionsAvailable: wctx.SubscriptionsAvailable,
	}
})
```

- [ ] **Step 5: Build + run all suites**

```
go build ./... && go test ./internal/cache/... ./internal/slack/... ./internal/ui/... ./cmd/slk/...
```

Expected: PASS. The behaviour change is: the threads view now reads from `thread_subscriptions`. For a freshly-launched binary on an existing cache, the table will be empty until the first reconnect/bootstrap populates it — that's expected and tested by Task 10's banner story.

- [ ] **Step 6: Commit**

```
git add cmd/slk/main.go internal/ui/app.go internal/ui/threadsview/model.go
git commit -m "main: surface SubscriptionsAvailable; swap threads view to ListSubscribedThreads"
```

---

## Task 10: Threads view banner UI

**Files:**
- Modify: `internal/ui/threadsview/model.go` (replace the Task-9 stub setter; add `subscriptionsAvailable` field; update `View` to render the banner).
- Modify: `internal/ui/threadsview/model_test.go` (banner-visibility tests).

The banner is a single line drawn at the top of the threads-view content area when `subscriptionsAvailable == false`. It appears regardless of whether `summaries` is empty or non-empty (the spec wants the user to see "we couldn't refresh — retrying" even if a stale local list is still rendered).

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/threadsview/model_test.go`:

```go
func TestView_RendersBannerWhenSubscriptionsUnavailable(t *testing.T) {
	m := New(map[string]string{}, "U1")
	m.SetSubscriptionsAvailable(false)
	out := m.View(10, 80)
	if !strings.Contains(out, "Threads list unavailable") {
		t.Errorf("expected banner in view, got:\n%s", out)
	}
}

func TestView_NoBannerWhenSubscriptionsAvailable(t *testing.T) {
	m := New(map[string]string{}, "U1")
	// Default is true; no need to call setter.
	out := m.View(10, 80)
	if strings.Contains(out, "Threads list unavailable") {
		t.Errorf("did not expect banner, got:\n%s", out)
	}
}

func TestView_BannerVisibleWithEmptySummaries(t *testing.T) {
	m := New(map[string]string{}, "U1")
	m.SetSubscriptionsAvailable(false)
	out := m.View(10, 80)
	// Banner should be visible even when summaries is empty (the
	// usual "no threads" placeholder gets pushed down or replaced).
	if !strings.Contains(out, "Threads list unavailable") {
		t.Errorf("expected banner with empty summaries, got:\n%s", out)
	}
}

func TestView_BannerVisibleWithSummaries(t *testing.T) {
	m := New(map[string]string{"U2": "alice"}, "U1")
	m.SetSummaries([]cache.ThreadSummary{
		{ChannelID: "C1", ChannelName: "general", ThreadTS: "1.0", ParentText: "hi", LastReplyTS: "2.0", LastReplyBy: "U2"},
	})
	m.SetSubscriptionsAvailable(false)
	out := m.View(20, 80)
	if !strings.Contains(out, "Threads list unavailable") {
		t.Errorf("expected banner with summaries present, got:\n%s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("expected summary content alongside banner, got:\n%s", out)
	}
}
```

If `cache` isn't already imported in `model_test.go`, add `"github.com/gammons/slk/internal/cache"`. The existing `model_test.go` already imports `strings`.

- [ ] **Step 2: Run tests, see fail**

```
go test ./internal/ui/threadsview/ -run TestView -v
```

Expected: FAIL — banner text not present.

- [ ] **Step 3: Implement the field + setter + View change**

In `internal/ui/threadsview/model.go`:

1. Add a field to the `Model` struct (next to `focused`):

```go
type Model struct {
	// ... existing fields ...

	// subscriptionsAvailable tracks whether Slack's
	// subscriptions.thread.list call succeeded most recently. When
	// false, View renders a one-line "Threads list unavailable"
	// banner above the list/empty-state. Default is true (optimistic).
	subscriptionsAvailable bool

	version int64
}
```

2. Initialise to `true` in `New`:

```go
func New(userNames map[string]string, selfUserID string) Model {
	return Model{
		userNames:              userNames,
		selfUserID:             selfUserID,
		channelNames:           map[string]string{},
		subscriptionsAvailable: true,
	}
}
```

3. Replace the Task-9 stub setter:

```go
// SetSubscriptionsAvailable records whether Slack's authoritative
// subscription state could be fetched most recently. false flips the
// "Threads list unavailable" banner on; true clears it.
func (m *Model) SetSubscriptionsAvailable(available bool) {
	if m.subscriptionsAvailable == available {
		return
	}
	m.subscriptionsAvailable = available
	m.dirty()
}
```

4. Update `View` to render the banner at the top, occupying the first line, with the rest of the rendering shifted down by one row:

```go
func (m *Model) View(height, width int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	// Reserve one line for the banner when subscriptions are
	// unavailable. The banner is muted-style, truncated to width if
	// needed.
	var bannerLine string
	bodyHeight := height
	if !m.subscriptionsAvailable {
		bannerText := "Threads list unavailable — Slack subscription state could not be fetched. slk will retry on the next reconnect."
		if w := lipgloss.Width(bannerText); w > width {
			// Truncate to width.
			runes := []rune(bannerText)
			for i := range runes {
				if lipgloss.Width(string(runes[:i+1])) > width {
					bannerText = string(runes[:i])
					break
				}
			}
		}
		bannerLine = mutedStyle().Render(bannerText)
		// Pad to full width so the next line starts cleanly.
		if pad := width - lipgloss.Width(bannerLine); pad > 0 {
			bannerLine += strings.Repeat(" ", pad)
		}
		bodyHeight = height - 1
		if bodyHeight < 0 {
			bodyHeight = 0
		}
	}

	// Body: the empty-state placeholder or the rendered rows. Mirror
	// the existing logic but render into bodyHeight, then prepend the
	// banner.
	var body string
	if len(m.summaries) == 0 {
		empty := mutedStyle().Render("no threads")
		body = lipgloss.Place(width, bodyHeight, lipgloss.Center, lipgloss.Center, empty)
	} else {
		lines := m.renderRows(width)
		if !m.hasSnapped || m.snappedSelection != m.selected {
			m.snapToSelected(bodyHeight, len(lines))
			m.snappedSelection = m.selected
			m.hasSnapped = true
		}
		maxOffset := len(lines) - bodyHeight
		if maxOffset < 0 {
			maxOffset = 0
		}
		if m.yOffset > maxOffset {
			m.yOffset = maxOffset
		}
		if m.yOffset < 0 {
			m.yOffset = 0
		}
		end := m.yOffset + bodyHeight
		if end > len(lines) {
			end = len(lines)
		}
		visible := lines[m.yOffset:end]
		if pad := bodyHeight - len(visible); pad > 0 {
			filler := blankLine(width)
			out := make([]string, 0, bodyHeight)
			out = append(out, visible...)
			for i := 0; i < pad; i++ {
				out = append(out, filler)
			}
			visible = out
		}
		body = strings.Join(visible, "\n")
	}

	if bannerLine == "" {
		return body
	}
	if bodyHeight == 0 {
		return bannerLine
	}
	return bannerLine + "\n" + body
}
```

- [ ] **Step 4: Run tests, see pass**

```
go test ./internal/ui/threadsview/ -run TestView -v
```

Expected: PASS for all four banner tests, and existing `View` tests should still pass (they all instantiate with default `subscriptionsAvailable=true`, so the banner is suppressed and the rendering matches the prior behaviour). If a pre-existing test was hard-coded to a specific output width and the new branch broke padding, fix the test rather than the implementation — the new behaviour with `available=true` should be byte-identical to the old.

- [ ] **Step 5: Run full ui suite**

```
go test ./internal/ui/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/ui/threadsview/model.go internal/ui/threadsview/model_test.go
git commit -m "threadsview: banner when subscription state is unavailable"
```

---

## Task 11: Delete `ListInvolvedThreads` and its tests

**Files:**
- Modify: `internal/cache/threads.go` (remove `ListInvolvedThreads`; keep `ThreadInvolvesUser`).
- Modify: `internal/cache/threads_test.go` (remove `TestListInvolvedThreads_*` tests; keep `TestThreadInvolvesUser_*` and the Task-5 `TestListSubscribedThreads_*` tests).

Confirm no callers remain before deleting:

- [ ] **Step 1: Confirm no callers reference `ListInvolvedThreads`**

```
grep -rn "ListInvolvedThreads" --include='*.go'
```

Expected: only matches inside `internal/cache/threads.go` (the definition) and the soon-to-be-removed test functions. If there are any other matches, that caller must be migrated to `ListSubscribedThreads` first (and the spec wants only one caller, the `SetThreadsListFetcher` closure swapped in Task 9).

- [ ] **Step 2: Delete the function**

Remove the entire `ListInvolvedThreads` function body and its leading doc comment from `internal/cache/threads.go`. Leave `ThreadInvolvesUser` alone. Leave `ListSubscribedThreads` alone.

- [ ] **Step 3: Delete the related tests**

In `internal/cache/threads_test.go`, remove these test functions and the `seedThreadFixtures` helper (the new tests use `seedSubscribedThreadFixtures` from Task 5):

- `TestListInvolvedThreads_IncludesAuthoredRepliedMentioned`
- `TestListInvolvedThreads_OrderingByLastReplyTS`
- `TestListInvolvedThreads_UnreadDoesNotChangeOrder`
- `TestListInvolvedThreads_PopulatesParentAndReplyCount`
- `TestListInvolvedThreads_MentionRequiresAngleBrackets`
- `TestListInvolvedThreads_ParentMissingFromCache`
- `TestListInvolvedThreads_PerWorkspaceIsolation`
- `seedThreadFixtures` (helper)

Keep:

- All `TestThreadInvolvesUser_*` tests
- All `TestListSubscribedThreads_*` tests
- `seedSubscribedThreadFixtures` helper (Task 5)
- `mustUpsertMsg` helper (Task 5)

- [ ] **Step 4: Build + run all suites**

```
go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/cache/threads.go internal/cache/threads_test.go
git commit -m "cache: remove obsolete ListInvolvedThreads heuristic"
```

---

## Task 12: Final verification

**Files:** none modified.

- [ ] **Step 1: Run the entire test suite**

```
go test ./...
```

Expected: PASS, including:
- `internal/cache/` — table, helpers, ListSubscribedThreads, ThreadInvolvesUser
- `internal/slack/` — ListThreadSubscriptions paging/cap/empty/error
- `internal/ui/threadsview/` — banner
- `cmd/slk/` — OnThreadMarked persistence, all 6 backfill tests

- [ ] **Step 2: Sanity-build the binary**

```
go build -o /tmp/slk-subs ./cmd/slk
```

Expected: clean build, binary produced.

- [ ] **Step 3: Manual smoke (operator)**

In a test workspace:

1. Wipe the local cache file (`~/.local/share/slk/<team-id>.db` or whatever the XDG path resolves to) to force a cold bootstrap.
2. Launch `SLK_DEBUG=1 ./slk-subs`.
3. Open the Threads view. Confirm it lists at least the threads visible in the official Slack client's Threads view, NOT just threads where the user has a cached message.
4. Pick a thread the user is subscribed to but has never replied to (e.g. the Cisco-hub thread from the spec). Confirm it appears with real parent text, not `(parent not loaded)`.
5. From the official Slack client, mark a thread as unread. Confirm slk's threads view shows it as unread within a few seconds (WS `thread_marked` should fire).
6. Kill slk's WS connection (e.g. drop the network). Confirm the next reconnect repopulates the list via `runSubscriptionPhase`.
7. Force `subscriptions.thread.list` to fail (e.g. point `apiBaseURL` at a 503'ing server, or rotate the cookie to break auth temporarily). Confirm the "Threads list unavailable" banner appears and clears on the next successful reconnect.

- [ ] **Step 4: Capture a short debug-log snippet**

`grep '\[backfill\]' slk-debug.log | grep subscription` should show `subscription-phase subs=N missing_parents=M dur_ms=...` lines for each connect.

- [ ] **Step 5: No commit — this is verification only.**

---

## Self-review checklist

After implementation completes, run these spec-coverage checks:

- [ ] **Goal 1: Threads view matches Slack's set.** Verified by manual smoke step 3.
- [ ] **Goal 2: Subscription state stays current via WS events.** Verified by manual smoke step 5 + `TestOnThreadMarked_UpsertsSubscription`.
- [ ] **Goal 3: Reconnect refreshes the full list.** Verified by manual smoke step 6 + `TestBackfillSubscriptions_PopulatesTable` and `_ReconcilesUnsubscribes`.
- [ ] **Goal 4: Failure surfaces a banner; no fallback to heuristic.** Verified by manual smoke step 7 + `TestView_RendersBannerWhenSubscriptionsUnavailable` and `TestBackfillSubscriptions_ErrorTriggersAvailabilityCallback`.
- [ ] **Migration is purely additive.** Verified by `TestMigrate_CreatesThreadSubscriptionsTable` (existing caches gain the empty table on first migrate).
- [ ] **`ThreadInvolvesUser` stays.** Verified by no edits in Task 11 + grep.
- [ ] **`ListInvolvedThreads` gone.** Verified by `grep -rn "ListInvolvedThreads"` returning zero results after Task 11.

