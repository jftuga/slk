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

## Task 1: Endpoint discovery

**Files:**
- Create: `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`

This task is **manual** — performed by the human operator using the official Slack web client in a real browser. The agent's job is to wait for the operator to drop the discovery notes into the file, then verify the file is well-formed before unblocking the rest of the plan.

- [ ] **Step 1: Open the Threads view in the official Slack web client**

In a Chromium-based browser, open `https://app.slack.com/client/<TEAM_ID>/`. Open DevTools → Network tab. Filter by `subscriptions.thread`. Click the sidebar "Threads" link. Note every request whose path starts with `subscriptions.thread`. Expected candidates: `subscriptions.thread.list`, `subscriptions.thread.getView`, `subscriptions.thread.history`. Take a screenshot or copy the request URLs.

- [ ] **Step 2: Capture one full request/response pair**

For the request that returns the threads list (largest response body, contains an array of subscriptions), use DevTools → "Copy as cURL" → paste into a scratch file. Strip the cookie/token to placeholders so the notes file is safe to commit.

- [ ] **Step 3: Trigger pagination if possible**

If the response includes `response_metadata.next_cursor` with a non-empty value, scroll the threads view (or click "load more"); record the second-page request to confirm cursor field name and how it's threaded back through the form fields.

- [ ] **Step 4: Trigger a subscribe/unsubscribe and record any WS events**

In Slack, mark a thread as unread, then as read, then "unsubscribe" if available. Watch DevTools → Network → WS → frames for any event types other than `thread_marked`. Record them. Also tail `slk-debug.log` after exercising the same flows against slk and grep `\[ws\] unknown event` — note any new event types.

- [ ] **Step 5: Write `docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md`**

Fill in the template from the plan's "Discovery output document" section above. Every section must have a concrete value — no `TODO`, no `<paste here>`. The sample response must be a real (token-scrubbed) JSON blob, pretty-printed.

- [ ] **Step 6: Verify the notes file is non-empty and contains the required sections**

Run from the worktree root:

```
test -s docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Endpoint$' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Request form fields$' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Pagination cursor location$' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Response shape' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Per-subscription item shape$' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Inactive subscriptions in response?$' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
grep -q '^## Sample full response' docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md && \
echo OK
```

Expected output: `OK`.

- [ ] **Step 7: Commit**

```
git add docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md
git commit -m "docs: subscriptions.thread.list endpoint discovery notes"
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

**Reference Task 1's notes file before writing any code.** If discovery found that the endpoint name is something other than `subscriptions.thread.list`, or that the cursor lives at a different JSON path, substitute the discovered values into both the implementation and the tests below. The structural shape of this task (one POST per page, loop until cursor is empty, hard cap, rate-limit retry) is independent of the field names.

For the rest of this task description, the assumed-from-spec values are used as placeholders:
- endpoint: `subscriptions.thread.list`
- cursor path: `response_metadata.next_cursor`
- response field name for the array: `subscriptions`
- per-item fields: `channel`, `thread_ts`, `last_read`, `active`

- [ ] **Step 1: Re-read the discovery notes**

```
cat docs/superpowers/notes/2026-05-12-subscriptions-thread-endpoint.md
```

If any of the placeholder values above don't match what's in the notes file, substitute the real values throughout the rest of this task.

- [ ] **Step 2: Write the failing tests**

Append to `internal/slack/client_test.go`:

```go
func TestListThreadSubscriptions_PaginatesUntilExhausted(t *testing.T) {
	var calls int
	var capturedCursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		capturedCursors = append(capturedCursors, r.PostForm.Get("cursor"))
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{
				"ok": true,
				"subscriptions": [
					{"channel": "C1", "thread_ts": "1700000000.000100", "last_read": "1700000000.000200", "active": true},
					{"channel": "C2", "thread_ts": "1700000001.000100", "last_read": "1700000001.000200", "active": true}
				],
				"response_metadata": {"next_cursor": "P2"}
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"ok": true,
				"subscriptions": [
					{"channel": "C3", "thread_ts": "1700000002.000100", "last_read": "1700000002.000200", "active": true}
				],
				"response_metadata": {"next_cursor": ""}
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
	if got[0].ChannelID != "C1" || got[2].ChannelID != "C3" {
		t.Errorf("got = %+v", got)
	}
	if capturedCursors[0] != "" || capturedCursors[1] != "P2" {
		t.Errorf("cursors = %v", capturedCursors)
	}
}

func TestListThreadSubscriptions_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true, "subscriptions": [], "response_metadata": {"next_cursor": ""}}`))
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
	// Server returns 100 subs per page with a perpetual next_cursor.
	// The client should stop after the hard cap (1000) and never call
	// the server an 11th time.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		var b []byte
		b = append(b, []byte(`{"ok": true, "subscriptions": [`)...)
		for i := 0; i < 100; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, []byte(`{"channel": "C", "thread_ts": "1.0", "last_read": "1.0", "active": true}`)...)
		}
		b = append(b, []byte(`], "response_metadata": {"next_cursor": "more"}}`)...)
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

(If `strings` isn't already imported in `client_test.go`, the test build will demand it — leave the file to `goimports` / the editor; the existing tests already import `strings`.)

- [ ] **Step 3: Run tests, see fail**

```
go test ./internal/slack/ -run TestListThreadSubscriptions -v
```

Expected: FAIL — `undefined: c.ListThreadSubscriptions`.

- [ ] **Step 4: Implement `ListThreadSubscriptions`**

Append to `internal/slack/client.go`. Place near the other hand-rolled paginated endpoints (e.g. just below `GetChannelSections` and `callChannelSectionsList`). The cache-layer `ThreadSubscription` type lives in `internal/cache` and we don't want to depend on it from `internal/slack`, so this method returns its own `ThreadSubscription` value type owned by `internal/slack` — the caller (in `cmd/slk/reconnect_backfill.go`) is responsible for adapting it into `cache.ThreadSubscription` rows before passing to `Reconcile`.

```go
// ThreadSubscription is one entry returned by ListThreadSubscriptions.
// Mirrors the JSON shape Slack ships in the subscriptions.thread.list
// response. Active=true means "subscribed for unread updates";
// Active=false rows may or may not appear depending on Slack's
// server-side filter (see ListThreadSubscriptions docs).
type ThreadSubscription struct {
	ChannelID string
	ThreadTS  string
	LastRead  string
	Active    bool
}

// listThreadSubscriptionsResponse decodes one page of
// subscriptions.thread.list. Field names match Slack's wire format —
// rename here if Task 1 discovery found different names.
type listThreadSubscriptionsResponse struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error"`
	Subscriptions []struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		LastRead string `json:"last_read"`
		Active   bool   `json:"active"`
	} `json:"subscriptions"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// listThreadSubscriptionsHardCap bounds how many subscriptions
// ListThreadSubscriptions will return per call. Protects against
// runaway requests if Slack ships a buggy cursor that never empties.
const listThreadSubscriptionsHardCap = 1000

// ListThreadSubscriptions fetches the workspace's full subscribed-
// threads list via Slack's internal subscriptions.thread.list endpoint
// (the same call the official web client makes when bootstrapping its
// Threads view). Paginates via response_metadata.next_cursor and stops
// at listThreadSubscriptionsHardCap subscriptions.
//
// Returns (nil, err) on network failure, non-2xx response, or
// ok=false JSON. Caller (the reconnect backfill phase) is expected
// to treat any error as "subscriptions unavailable" and surface the
// UI banner.
func (c *Client) ListThreadSubscriptions(ctx context.Context) ([]ThreadSubscription, error) {
	var all []ThreadSubscription
	cursor := ""
	for {
		body, err := c.callListThreadSubscriptions(ctx, cursor)
		if err != nil {
			return nil, err
		}
		var resp listThreadSubscriptionsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing subscriptions.thread.list: %w (body=%s)", err, truncateForLog(body))
		}
		if !resp.OK {
			return nil, fmt.Errorf("subscriptions.thread.list: %s (body=%s)", resp.Error, truncateForLog(body))
		}
		for _, sub := range resp.Subscriptions {
			all = append(all, ThreadSubscription{
				ChannelID: sub.Channel,
				ThreadTS:  sub.ThreadTS,
				LastRead:  sub.LastRead,
				Active:    sub.Active,
			})
			if len(all) >= listThreadSubscriptionsHardCap {
				debuglog.Backfill("ListThreadSubscriptions: hit hard cap %d, stopping", listThreadSubscriptionsHardCap)
				return all, nil
			}
		}
		if resp.ResponseMetadata.NextCursor == "" || resp.ResponseMetadata.NextCursor == cursor {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}
	return all, nil
}

func (c *Client) callListThreadSubscriptions(ctx context.Context, cursor string) ([]byte, error) {
	form := url.Values{}
	if cursor != "" {
		form.Set("cursor", cursor)
	}
	// Request the largest reasonable page size to minimize round-trips.
	// Slack accepts any value up to a server-side cap (~100 in practice).
	form.Set("count", "100")
	return c.postForm(ctx, "subscriptions.thread.list", form)
}
```

You may also need to add `"github.com/gammons/slk/internal/debuglog"` to the imports of `internal/slack/client.go` if not already present. Check with:

```
grep '"github.com/gammons/slk/internal/debuglog"' internal/slack/client.go
```

If missing, add it.

- [ ] **Step 5: Run tests, see pass**

```
go test ./internal/slack/ -run TestListThreadSubscriptions -v
```

Expected: PASS for all four tests.

- [ ] **Step 6: Run full slack-package suite**

```
go test ./internal/slack/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add internal/slack/client.go internal/slack/client_test.go
git commit -m "slack: ListThreadSubscriptions with pagination + hard cap"
```

---

## Task 7: Extend `OnThreadMarked` to persist subscription state

**Files:**
- Modify: `cmd/slk/main.go` (the `OnThreadMarked` method on `rtmEventHandler`, currently at lines 2787-2801).
- Modify: `cmd/slk/event_handler_test.go` (existing per-handler tests live here — same package `main`; mirrors the style of `TestOnConversationOpened_*` and `TestOnMessage_*`).

`active = !read` per the dispatch in `internal/slack/events.go:319-329`. Every WS `thread_marked` should result in an upsert to `thread_subscriptions` reflecting the new state.

- [ ] **Step 1: Find the existing `OnThreadMarked` test, if any**

```
grep -rn "OnThreadMarked" cmd/slk/
```

If there's already a test (e.g. `TestOnThreadMarked_*`), extend it. If not, this task creates a new one.

- [ ] **Step 2: Write the failing test**

Add to `cmd/slk/event_handler_test.go` (existing file; `package main`; uses the same `newTestDB` helper from `reconnect_backfill_test.go`):

```go
func TestOnThreadMarked_UpsertsSubscription(t *testing.T) {
	db := newTestDB(t) // helper from reconnect_backfill_test.go (package main)
	h := &rtmEventHandler{
		db:          db,
		workspaceID: "T1",
		isActive:    func() bool { return true },
		// program/notifier left nil; OnThreadMarked nil-checks before
		// dispatching the UI message.
	}

	// Read=false means the thread is now unread (active=true in
	// thread_subscriptions terms).
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

	// Now mark read; the row should flip to inactive.
	h.OnThreadMarked("C1", "1700000100.000000", "1700000150.000000", true)
	got, _ = db.ListActiveThreadSubscriptions("T1")
	if len(got) != 0 {
		t.Fatalf("expected 0 active after read=true, got %d", len(got))
	}
}
```

(If `newTestDB` is in the same `package main` as the handler, this test can use it directly. Otherwise, copy the helper or extract it.)

- [ ] **Step 3: Run test, see fail**

```
go test ./cmd/slk/ -run TestOnThreadMarked_UpsertsSubscription -v
```

Expected: FAIL — no thread_subscriptions row exists.

- [ ] **Step 4: Extend `OnThreadMarked`**

In `cmd/slk/main.go`, replace the body of `func (h *rtmEventHandler) OnThreadMarked(...)`:

```go
func (h *rtmEventHandler) OnThreadMarked(channelID, threadTS, ts string, read bool) {
	if h.isActive != nil && !h.isActive() {
		// Inactive workspace: skip both persistence and UI dispatch.
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

If `debuglog` isn't imported in `cmd/slk/main.go`, add the import — most of the file already uses it, so the import should already be there.

- [ ] **Step 5: Run test, see pass**

```
go test ./cmd/slk/ -run TestOnThreadMarked_UpsertsSubscription -v
```

Expected: PASS.

- [ ] **Step 6: Run full cmd/slk suite to confirm no regressions**

```
go test ./cmd/slk/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add cmd/slk/main.go cmd/slk/event_handler_test.go
git commit -m "main: persist thread subscription state on WS thread_marked"
```

---

## Task 8: Backfiller `runSubscriptionPhase`

**Files:**
- Modify: `cmd/slk/reconnect_backfill.go` (extend `historyFetcher` interface; add `runSubscriptionPhase`; thread an `availableCb` into `backfiller` so failure can flip `wctx.SubscriptionsAvailable` without `cmd/slk/reconnect_backfill.go` reaching into the `WorkspaceContext` type).
- Modify: `cmd/slk/reconnect_backfill_test.go` (extend `fakeHistory` with `ListThreadSubscriptions`; add four new tests).

`*slackclient.Client` already implicitly satisfies the new interface method after Task 6 lands.

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
	ListThreadSubscriptions(ctx context.Context) ([]slackclient.ThreadSubscription, error)
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
	subscriptionsResponse []slackclient.ThreadSubscription
	subscriptionsErr      error
	subscriptionsCalls    int
}

func (f *fakeHistory) ListThreadSubscriptions(ctx context.Context) ([]slackclient.ThreadSubscription, error) {
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

Append to `cmd/slk/reconnect_backfill_test.go`:

```go
func TestBackfillSubscriptions_PopulatesTable(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{}, // no channels
		subscriptionsResponse: []slackclient.ThreadSubscription{
			{ChannelID: "C1", ThreadTS: "1700000100.000000", LastRead: "1700000150.000000", Active: true},
			{ChannelID: "C2", ThreadTS: "1700000200.000000", LastRead: "1700000250.000000", Active: true},
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

func TestBackfillSubscriptions_FetchesParentsForUncachedThreads(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	// Thread X is in the subscription list but no message rows are
	// cached for it. The backfiller should call GetReplies(C1, X) so
	// the parent gets fetched.
	fake := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{},
		subscriptionsResponse: []slackclient.ThreadSubscription{
			{ChannelID: "C1", ThreadTS: "1700000100.000000", LastRead: "1700000150.000000", Active: true},
		},
		repliesResponses: map[string][]slack.Message{
			"1700000100.000000": {
				{Timestamp: "1700000100.000000", User: "U2", Text: "parent X", ThreadTimestamp: "1700000100.000000"},
				{Timestamp: "1700000200.000000", User: "U3", Text: "reply X1", ThreadTimestamp: "1700000100.000000"},
			},
		},
	}
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, nil)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}

	// Parent should now be cached.
	msgs, err := db.GetThreadReplies("C1", "1700000100.000000")
	if err != nil {
		t.Fatalf("GetThreadReplies: %v", err)
	}
	if len(msgs) < 1 {
		t.Fatalf("parent + replies were not cached: %v", msgs)
	}

	// And exactly one GetReplies call should have been made.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.repliesCalls) != 1 {
		t.Fatalf("want 1 GetReplies call, got %d: %v", len(fake.repliesCalls), fake.repliesCalls)
	}
}

func TestBackfillSubscriptions_DoesNotRefetchParentsAlreadyCached(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	// Pre-seed the parent.
	if err := db.UpsertMessage(cache.Message{
		TS: "1700000100.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2",
		Text: "parent", ThreadTS: "1700000100.000000",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	fake := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{},
		subscriptionsResponse: []slackclient.ThreadSubscription{
			{ChannelID: "C1", ThreadTS: "1700000100.000000", LastRead: "1700000150.000000", Active: true},
		},
	}
	bf := newBackfiller(fake, db, "T1", "U1", nil, 4, 500, nil)
	if err := bf.runSubscriptionPhase(context.Background()); err != nil {
		t.Fatalf("runSubscriptionPhase: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.repliesCalls) != 0 {
		t.Fatalf("expected 0 GetReplies calls when parent is cached, got %d: %v", len(fake.repliesCalls), fake.repliesCalls)
	}
}

func TestBackfillSubscriptions_ReconcilesUnsubscribes(t *testing.T) {
	db := newTestDB(t)
	// Seed a local subscription that's no longer in the server's fresh list.
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
	fake := &fakeHistory{
		subscriptionsResponse: []slackclient.ThreadSubscription{
			{ChannelID: "C2", ThreadTS: "1700000300.000000", LastRead: "1700000350.000000", Active: true},
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
// list, reconciles the local cache against it, and (best-effort)
// fetches parents for any subscribed thread whose parent isn't in the
// local messages cache. Side effects:
//
//  1. Local thread_subscriptions table reflects the server's
//     authoritative state at the moment of the call.
//  2. b.availableCb (if non-nil) is called with true on success or
//     false on error so the UI banner can reflect availability.
//  3. Parent messages for previously-uncached subscribed threads are
//     fetched via GetReplies through the existing concurrency pool.
//
// Errors from the API call are returned to the caller; per-thread
// parent-fetch failures are logged and skipped (one bad thread does
// not abort the pass).
func (b *backfiller) runSubscriptionPhase(ctx context.Context) error {
	start := time.Now()
	subs, err := b.client.ListThreadSubscriptions(ctx)
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

	// Adapt internal/slack types into cache.ThreadSubscription rows.
	fresh := make([]cache.ThreadSubscription, 0, len(subs))
	for _, s := range subs {
		// Filter to active=true so tombstones don't get re-promoted.
		// Per the Implementation discovery items in the spec: if the
		// endpoint returns inactive rows, this filter still does the
		// right thing — Reconcile tombstones any local active row not
		// present in `fresh`.
		if !s.Active {
			continue
		}
		fresh = append(fresh, cache.ThreadSubscription{
			WorkspaceID: b.workspaceID,
			ChannelID:   s.ChannelID,
			ThreadTS:    s.ThreadTS,
			LastRead:    s.LastRead,
			Active:      true,
		})
	}
	if err := b.db.ReconcileThreadSubscriptions(b.workspaceID, fresh); err != nil {
		debuglog.Backfill("team=%s reconcile err=%v", b.workspaceID, err)
		return err
	}

	// Fetch parents for any subscribed thread missing from the
	// messages cache. The "missing parent" probe is a single
	// db.GetMessage call per thread; cheap.
	var missing []threadKey
	for _, s := range fresh {
		exists, err := b.db.HasMessage(s.ChannelID, s.ThreadTS)
		if err != nil {
			debuglog.Backfill("team=%s HasMessage(%s/%s) err=%v", b.workspaceID, s.ChannelID, s.ThreadTS, err)
			continue
		}
		if !exists {
			missing = append(missing, threadKey{ChannelID: s.ChannelID, ThreadTS: s.ThreadTS})
		}
	}

	sem := make(chan struct{}, b.concurrency)
	var wg sync.WaitGroup
	for _, k := range missing {
		wg.Add(1)
		go func(k threadKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := b.backfillOneThread(ctx, k); err != nil {
				debuglog.Backfill("team=%s subscription-phase parent-fetch %s/%s err=%v",
					b.workspaceID, k.ChannelID, k.ThreadTS, err)
			}
		}(k)
	}
	wg.Wait()

	debuglog.Backfill("team=%s subscription-phase subs=%d missing_parents=%d dur_ms=%d",
		b.workspaceID, len(fresh), len(missing), time.Since(start).Milliseconds())
	return nil
}
```

This depends on a new `db.HasMessage(channelID, ts string) (bool, error)` helper. Add it to `internal/cache/messages.go`:

```go
// HasMessage reports whether a message with the given (channel_id, ts)
// exists in the cache and isn't tombstoned. Used by the reconnect
// backfill to skip parent-fetches for threads already cached.
func (db *DB) HasMessage(channelID, ts string) (bool, error) {
	const q = `SELECT 1 FROM messages WHERE channel_id=? AND ts=? AND is_deleted=0 LIMIT 1`
	var one int
	err := db.conn.QueryRow(q, channelID, ts).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("HasMessage: %w", err)
	}
	return true, nil
}
```

(Add `"database/sql"` to `messages.go` imports if not already present.)

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
git add cmd/slk/reconnect_backfill.go cmd/slk/reconnect_backfill_test.go cmd/slk/main.go internal/cache/messages.go
git commit -m "backfill: subscription phase populates and reconciles thread_subscriptions"
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

