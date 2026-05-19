# Read-State Sync Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate per-channel read state into a single SQLite source of truth, eliminate the three-store divergence, and add reconnect catch-up so channels stay in sync with the official Slack client.

**Architecture:** SQLite is the only store for `last_read_ts` and a new boolean `has_unread`. All read-state writes flow through a single API (`UpdateChannelReadState` / `BatchUpdateChannelReadState`). All UI consumers read via `GetWorkspaceReadState` at render time. `WorkspaceContext.LastReadMap` and the per-channel `LastReadTS` / `UnreadCount` fields are removed. The integer unread count is replaced with a boolean; section aggregates become counts-of-channels-with-unreads.

**Tech Stack:** Go 1.26, modernc.org/sqlite (WAL), bubbletea, lipgloss. Plain `testing.T` (no testify, no table-driven helpers). Tests use `cache.New(":memory:")` or `newTestDB(t)` from `cmd/slk/reconnect_backfill_test.go:133-145`.

**Spec:** `docs/superpowers/specs/2026-05-17-read-state-sync-design.md`

---

## File structure

**New files:**

- `internal/cache/channels_read_state.go` — `ReadState`, `ChannelReadStateUpdate` types, `UpdateChannelReadState`, `BatchUpdateChannelReadState`, `GetChannelReadState`, `GetWorkspaceReadState`, `WorkspacesWithUnreads`.
- `internal/cache/channels_read_state_test.go` — unit tests for the new API including the clobber-regression test.
- `cmd/slk/event_handler_marked_test.go` — new tests for `OnChannelMarked` calling the new API.

**Modified files:**

- `internal/cache/db.go` — add `has_unread` column via `addColumnIfMissing`.
- `internal/cache/channels.go` — fix `UpsertChannel` (drop `last_read_ts`/`unread_count` from `ON CONFLICT DO UPDATE`), remove `LastReadTS` and `UnreadCount` fields from `cache.Channel`, delete dead `UpdateUnreadCount`/`UpdateLastReadTS`/`GetLastReadTS`.
- `internal/cache/channels_test.go` — remove tests for deleted functions; update `TestUpsertAndGetChannel` for the slimmer struct.
- `internal/slack/client.go` — drop now-unused `Count` field on `UnreadInfo`? (Kept — see Task 14 note.)
- `cmd/slk/main.go` — bootstrap migration to `BatchUpdateChannelReadState`; remove `LastReadMap`; route all read-state writes through the new API.
- `cmd/slk/channelitem.go` — no behavioral change after the struct/upsert fix; the zero-value bootstrap clobber goes away automatically.
- `cmd/slk/reconnect_backfill.go` — `runChannelPhase` now calls `BatchUpdateChannelReadState`.
- `cmd/slk/reconnect_backfill_test.go` — new test asserting batch write; extend `TestBackfill_OvernightSuspendScenario` with channel D.
- `cmd/slk/event_handler_test.go` — update existing tests for the new write path.
- `internal/ui/app.go` — emit `ReadStateChangedMsg`; remove `MarkUnread`/`ClearUnread`/`SetUnreadCount` callers; route through DB API.
- `internal/ui/sidebar/model.go` — delete `MarkUnread`/`ClearUnread`/`SetUnreadCount`; add `ReadStateReader` provider; `View()` consults DB; `ChannelItem` loses `UnreadCount`/`LastReadTS`; section aggregate counts channels.
- `internal/ui/sidebar/model_test.go` — delete/rewrite tests for removed methods.
- `internal/ui/workspace/model.go` — rail dot driven by `WorkspacesWithUnreads`.

**Deprecated but kept:**

- `unread_count` column on `channels` table — kept one release per spec, never read, never written.

---

## Phase 1: Cache layer foundation

### Task 1: Add `has_unread` column migration

**Files:**
- Modify: `internal/cache/db.go:182-190`
- Test: `internal/cache/db_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/cache/db_test.go`:

```go
func TestMigration_AddsHasUnreadColumn(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	rows, err := db.conn.Query("PRAGMA table_info(channels)")
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "has_unread" {
			found = true
			if ctype != "INTEGER" {
				t.Errorf("has_unread type = %q, want INTEGER", ctype)
			}
			if notnull != 1 {
				t.Errorf("has_unread NOT NULL = %d, want 1", notnull)
			}
			if !dflt.Valid || dflt.String != "0" {
				t.Errorf("has_unread default = %v, want 0", dflt)
			}
		}
	}
	if !found {
		t.Fatal("has_unread column not added")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cache/ -run TestMigration_AddsHasUnreadColumn -v`
Expected: FAIL with `has_unread column not added`.

- [ ] **Step 3: Add the migration**

Edit `internal/cache/db.go` after line 189 (after `latest_synced_ts` block, before `return nil`):

```go
	if err := db.addColumnIfMissing("channels", "has_unread",
		"ALTER TABLE channels ADD COLUMN has_unread INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
```

Also add `has_unread INTEGER NOT NULL DEFAULT 0` to the inline `CREATE TABLE channels` schema at `db.go:86-98` so fresh databases get the column directly. Insert it after `unread_count INTEGER NOT NULL DEFAULT 0,` on line 95:

```sql
	CREATE TABLE IF NOT EXISTS channels (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		name TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'channel',
		topic TEXT NOT NULL DEFAULT '',
		is_member INTEGER NOT NULL DEFAULT 0,
		is_starred INTEGER NOT NULL DEFAULT 0,
		last_read_ts TEXT NOT NULL DEFAULT '',
		unread_count INTEGER NOT NULL DEFAULT 0,
		has_unread INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id)
	);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cache/ -run TestMigration_AddsHasUnreadColumn -v`
Expected: PASS.

Run: `go test ./internal/cache/ -v` to confirm no other regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/db.go internal/cache/db_test.go
git commit -m "cache: add has_unread column to channels table"
```

---

### Task 2: Add `ReadState` and `ChannelReadStateUpdate` types + skeleton functions

**Files:**
- Create: `internal/cache/channels_read_state.go`

- [ ] **Step 1: Create the new file with type definitions and stub bodies**

```go
package cache

import (
	"database/sql"
	"fmt"
)

// ReadState captures the per-channel read-state values that drive the
// unread dot and "new messages" line. It is the canonical type for
// passing read state across package boundaries.
type ReadState struct {
	LastReadTS string
	HasUnread  bool
}

// ChannelReadStateUpdate is one entry in a batched read-state write.
// LastReadTS == "" means "preserve the existing last_read_ts" (used by
// events that update has_unread only, e.g. new-message arrivals).
type ChannelReadStateUpdate struct {
	ChannelID  string
	LastReadTS string
	HasUnread  bool
}

// UpdateChannelReadState atomically updates the per-channel read state.
// If lastReadTS == "", the existing last_read_ts is preserved. This is
// the ONLY function permitted to modify read state after bootstrap.
func (db *DB) UpdateChannelReadState(channelID, lastReadTS string, hasUnread bool) error {
	return fmt.Errorf("not implemented")
}

// BatchUpdateChannelReadState writes multiple updates in a single
// transaction. Used by bootstrap and reconnect catch-up paths.
func (db *DB) BatchUpdateChannelReadState(updates []ChannelReadStateUpdate) error {
	return fmt.Errorf("not implemented")
}

// GetChannelReadState returns the read state for a single channel.
// A missing row yields a zero-valued ReadState and a nil error.
func (db *DB) GetChannelReadState(channelID string) (ReadState, error) {
	return ReadState{}, fmt.Errorf("not implemented")
}

// GetWorkspaceReadState returns channelID -> ReadState for every
// channel in the workspace. Single batched query. Called by the
// sidebar View() at render time.
func (db *DB) GetWorkspaceReadState(workspaceID string) (map[string]ReadState, error) {
	return nil, fmt.Errorf("not implemented")
}

// WorkspacesWithUnreads returns the set of workspace IDs with at least
// one has_unread=true channel. Used by the workspace rail.
func (db *DB) WorkspacesWithUnreads() ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}

var _ = sql.ErrNoRows // keep sql import for later use
```

- [ ] **Step 2: Verify the package still builds**

Run: `go build ./internal/cache/`
Expected: build succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/cache/channels_read_state.go
git commit -m "cache: add read-state API skeleton"
```

---

### Task 3: Implement `UpdateChannelReadState` (TDD)

**Files:**
- Modify: `internal/cache/channels_read_state.go`
- Create: `internal/cache/channels_read_state_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/cache/channels_read_state_test.go`:

```go
package cache

import (
	"testing"
)

func newRSChannel(t *testing.T, db *DB, id, workspaceID string) {
	t.Helper()
	if err := db.UpsertWorkspace(Workspace{ID: workspaceID, Name: "ws"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	if err := db.UpsertChannel(Channel{ID: id, WorkspaceID: workspaceID, Name: id, Type: "channel"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
}

func TestUpdateChannelReadState_WritesBothColumns(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")

	if err := db.UpdateChannelReadState("C1", "1700000000.000001", true); err != nil {
		t.Fatalf("UpdateChannelReadState: %v", err)
	}
	state, err := db.GetChannelReadState("C1")
	if err != nil {
		t.Fatalf("GetChannelReadState: %v", err)
	}
	if state.LastReadTS != "1700000000.000001" {
		t.Errorf("LastReadTS = %q, want %q", state.LastReadTS, "1700000000.000001")
	}
	if !state.HasUnread {
		t.Errorf("HasUnread = false, want true")
	}
}

func TestUpdateChannelReadState_EmptyTSPreservesExisting(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")

	if err := db.UpdateChannelReadState("C1", "1700000000.000001", false); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := db.UpdateChannelReadState("C1", "", true); err != nil {
		t.Fatalf("second update: %v", err)
	}
	state, err := db.GetChannelReadState("C1")
	if err != nil {
		t.Fatalf("GetChannelReadState: %v", err)
	}
	if state.LastReadTS != "1700000000.000001" {
		t.Errorf("LastReadTS = %q, want preserved %q", state.LastReadTS, "1700000000.000001")
	}
	if !state.HasUnread {
		t.Errorf("HasUnread = false, want true")
	}
}

func TestUpdateChannelReadState_Idempotent(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")

	for i := 0; i < 3; i++ {
		if err := db.UpdateChannelReadState("C1", "1700000000.000001", true); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	state, _ := db.GetChannelReadState("C1")
	if state.LastReadTS != "1700000000.000001" || !state.HasUnread {
		t.Errorf("state = %+v after 3 writes", state)
	}
}
```

If `UpsertWorkspace` doesn't exist, use the actual constructor — check `internal/cache/workspaces.go`. The test helper may need to use a direct `db.conn.Exec("INSERT INTO workspaces ...")` instead.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache/ -run TestUpdateChannelReadState -v`
Expected: FAIL with `not implemented` errors.

- [ ] **Step 3: Implement the function**

Replace the stub in `internal/cache/channels_read_state.go`:

```go
func (db *DB) UpdateChannelReadState(channelID, lastReadTS string, hasUnread bool) error {
	var q string
	var args []any
	if lastReadTS == "" {
		q = `UPDATE channels SET has_unread = ? WHERE id = ?`
		args = []any{boolToInt(hasUnread), channelID}
	} else {
		q = `UPDATE channels SET last_read_ts = ?, has_unread = ? WHERE id = ?`
		args = []any{lastReadTS, boolToInt(hasUnread), channelID}
	}
	if _, err := db.conn.Exec(q, args...); err != nil {
		return fmt.Errorf("updating channel read state: %w", err)
	}
	return nil
}

func (db *DB) GetChannelReadState(channelID string) (ReadState, error) {
	var lastReadTS string
	var hasUnread int
	err := db.conn.QueryRow(
		`SELECT last_read_ts, has_unread FROM channels WHERE id = ?`,
		channelID,
	).Scan(&lastReadTS, &hasUnread)
	if err == sql.ErrNoRows {
		return ReadState{}, nil
	}
	if err != nil {
		return ReadState{}, fmt.Errorf("getting channel read state: %w", err)
	}
	return ReadState{LastReadTS: lastReadTS, HasUnread: hasUnread == 1}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/ -run TestUpdateChannelReadState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/channels_read_state.go internal/cache/channels_read_state_test.go
git commit -m "cache: implement UpdateChannelReadState and GetChannelReadState"
```

---

### Task 4: Implement `BatchUpdateChannelReadState` (TDD)

**Files:**
- Modify: `internal/cache/channels_read_state.go`
- Modify: `internal/cache/channels_read_state_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/cache/channels_read_state_test.go`:

```go
func TestBatchUpdateChannelReadState_WritesAll(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")
	newRSChannel(t, db, "C2", "T1")
	newRSChannel(t, db, "C3", "T1")

	updates := []ChannelReadStateUpdate{
		{ChannelID: "C1", LastReadTS: "1.0001", HasUnread: true},
		{ChannelID: "C2", LastReadTS: "1.0002", HasUnread: false},
		{ChannelID: "C3", LastReadTS: "", HasUnread: true},
	}
	if err := db.BatchUpdateChannelReadState(updates); err != nil {
		t.Fatalf("BatchUpdateChannelReadState: %v", err)
	}
	s1, _ := db.GetChannelReadState("C1")
	if s1.LastReadTS != "1.0001" || !s1.HasUnread {
		t.Errorf("C1 = %+v", s1)
	}
	s2, _ := db.GetChannelReadState("C2")
	if s2.LastReadTS != "1.0002" || s2.HasUnread {
		t.Errorf("C2 = %+v", s2)
	}
	s3, _ := db.GetChannelReadState("C3")
	if s3.LastReadTS != "" || !s3.HasUnread {
		t.Errorf("C3 = %+v (LastReadTS should be preserved empty)", s3)
	}
}

func TestBatchUpdateChannelReadState_Transactional(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")

	// Seed C1
	if err := db.UpdateChannelReadState("C1", "1.0", false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Empty batch is a no-op and returns nil.
	if err := db.BatchUpdateChannelReadState(nil); err != nil {
		t.Errorf("nil batch: %v", err)
	}
	if err := db.BatchUpdateChannelReadState([]ChannelReadStateUpdate{}); err != nil {
		t.Errorf("empty batch: %v", err)
	}
	// Original state untouched
	s, _ := db.GetChannelReadState("C1")
	if s.LastReadTS != "1.0" || s.HasUnread {
		t.Errorf("after empty batch C1 = %+v", s)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache/ -run TestBatchUpdateChannelReadState -v`
Expected: FAIL with `not implemented`.

- [ ] **Step 3: Implement the function**

Replace the stub in `internal/cache/channels_read_state.go`:

```go
func (db *DB) BatchUpdateChannelReadState(updates []ChannelReadStateUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin batch read-state tx: %w", err)
	}
	stmtBoth, err := tx.Prepare(`UPDATE channels SET last_read_ts = ?, has_unread = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare both: %w", err)
	}
	defer stmtBoth.Close()
	stmtFlag, err := tx.Prepare(`UPDATE channels SET has_unread = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare flag: %w", err)
	}
	defer stmtFlag.Close()

	for _, u := range updates {
		if u.LastReadTS == "" {
			if _, err := stmtFlag.Exec(boolToInt(u.HasUnread), u.ChannelID); err != nil {
				tx.Rollback()
				return fmt.Errorf("batch flag for %s: %w", u.ChannelID, err)
			}
		} else {
			if _, err := stmtBoth.Exec(u.LastReadTS, boolToInt(u.HasUnread), u.ChannelID); err != nil {
				tx.Rollback()
				return fmt.Errorf("batch both for %s: %w", u.ChannelID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch read-state: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/ -run TestBatchUpdateChannelReadState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/channels_read_state.go internal/cache/channels_read_state_test.go
git commit -m "cache: implement BatchUpdateChannelReadState"
```

---

### Task 5: Implement `GetWorkspaceReadState` and `WorkspacesWithUnreads` (TDD)

**Files:**
- Modify: `internal/cache/channels_read_state.go`
- Modify: `internal/cache/channels_read_state_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/cache/channels_read_state_test.go`:

```go
func TestGetWorkspaceReadState_ReturnsAllChannels(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")
	newRSChannel(t, db, "C2", "T1")
	newRSChannel(t, db, "C3", "T1")
	newRSChannel(t, db, "C4", "T2") // different workspace

	_ = db.UpdateChannelReadState("C1", "1.0001", true)
	_ = db.UpdateChannelReadState("C2", "1.0002", false)
	// C3 untouched — defaults

	got, err := db.GetWorkspaceReadState("T1")
	if err != nil {
		t.Fatalf("GetWorkspaceReadState: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3 (C3 must be included with defaults): %+v", len(got), got)
	}
	if got["C1"].LastReadTS != "1.0001" || !got["C1"].HasUnread {
		t.Errorf("C1 = %+v", got["C1"])
	}
	if got["C2"].LastReadTS != "1.0002" || got["C2"].HasUnread {
		t.Errorf("C2 = %+v", got["C2"])
	}
	if got["C3"].LastReadTS != "" || got["C3"].HasUnread {
		t.Errorf("C3 default = %+v", got["C3"])
	}
	if _, ok := got["C4"]; ok {
		t.Errorf("C4 from other workspace should not be returned")
	}
}

func TestWorkspacesWithUnreads(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")
	newRSChannel(t, db, "C2", "T2")
	newRSChannel(t, db, "C3", "T3")

	_ = db.UpdateChannelReadState("C1", "1.0", true)
	_ = db.UpdateChannelReadState("C3", "1.0", true)
	// C2/T2 has no unreads

	got, err := db.WorkspacesWithUnreads()
	if err != nil {
		t.Fatalf("WorkspacesWithUnreads: %v", err)
	}
	want := map[string]bool{"T1": true, "T3": true}
	if len(got) != 2 {
		t.Fatalf("got %d ids, want 2: %v", len(got), got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected workspace %q", id)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache/ -run "TestGetWorkspaceReadState|TestWorkspacesWithUnreads" -v`
Expected: FAIL with `not implemented`.

- [ ] **Step 3: Implement the functions**

Replace the stubs in `internal/cache/channels_read_state.go`:

```go
func (db *DB) GetWorkspaceReadState(workspaceID string) (map[string]ReadState, error) {
	rows, err := db.conn.Query(
		`SELECT id, last_read_ts, has_unread FROM channels WHERE workspace_id = ?`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("query workspace read state: %w", err)
	}
	defer rows.Close()
	out := make(map[string]ReadState)
	for rows.Next() {
		var id, lastRead string
		var hasUnread int
		if err := rows.Scan(&id, &lastRead, &hasUnread); err != nil {
			return nil, fmt.Errorf("scan workspace read state: %w", err)
		}
		out[id] = ReadState{LastReadTS: lastRead, HasUnread: hasUnread == 1}
	}
	return out, rows.Err()
}

func (db *DB) WorkspacesWithUnreads() ([]string, error) {
	rows, err := db.conn.Query(
		`SELECT DISTINCT workspace_id FROM channels WHERE has_unread = 1`,
	)
	if err != nil {
		return nil, fmt.Errorf("query workspaces with unreads: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan workspace id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
```

Also remove the `var _ = sql.ErrNoRows` placeholder line — `sql` is now actually used.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/ -v`
Expected: ALL PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/channels_read_state.go internal/cache/channels_read_state_test.go
git commit -m "cache: implement GetWorkspaceReadState and WorkspacesWithUnreads"
```

---

### Task 6: Fix `UpsertChannel` to never clobber read state + clobber-regression test

**Files:**
- Modify: `internal/cache/channels.go:21-41`
- Modify: `internal/cache/channels_read_state_test.go`

- [ ] **Step 1: Write the failing clobber-regression test**

Append to `internal/cache/channels_read_state_test.go`:

```go
func TestUpsertChannel_DoesNotClobberReadState(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	newRSChannel(t, db, "C1", "T1")

	// Set read state.
	if err := db.UpdateChannelReadState("C1", "1700000000.000001", true); err != nil {
		t.Fatalf("UpdateChannelReadState: %v", err)
	}

	// Re-upsert the channel with zero-value LastReadTS/UnreadCount.
	// This mirrors what bootstrap (upsertChannelInDB) does today.
	if err := db.UpsertChannel(Channel{
		ID:          "C1",
		WorkspaceID: "T1",
		Name:        "renamed",
		Type:        "channel",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	state, err := db.GetChannelReadState("C1")
	if err != nil {
		t.Fatalf("GetChannelReadState: %v", err)
	}
	if state.LastReadTS != "1700000000.000001" {
		t.Errorf("LastReadTS = %q, want preserved %q (clobber regression!)", state.LastReadTS, "1700000000.000001")
	}
	if !state.HasUnread {
		t.Errorf("HasUnread = false after upsert (clobber regression!)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cache/ -run TestUpsertChannel_DoesNotClobberReadState -v`
Expected: FAIL — `last_read_ts` reset to empty string.

- [ ] **Step 3: Fix `UpsertChannel`**

Edit `internal/cache/channels.go:21-41` — remove `last_read_ts` and `unread_count` from the `ON CONFLICT DO UPDATE SET` clause. The `INSERT` still writes them (for fresh rows the zero values are fine and the schema defaults would handle it anyway).

```go
func (db *DB) UpsertChannel(ch Channel) error {
	_, err := db.conn.Exec(`
		INSERT INTO channels (id, workspace_id, name, type, topic, is_member, is_starred, last_read_ts, unread_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			type=excluded.type,
			topic=excluded.topic,
			is_member=excluded.is_member,
			is_starred=excluded.is_starred,
			updated_at=excluded.updated_at
	`, ch.ID, ch.WorkspaceID, ch.Name, ch.Type, ch.Topic,
		boolToInt(ch.IsMember), boolToInt(ch.IsStarred),
		ch.LastReadTS, ch.UnreadCount, ch.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting channel: %w", err)
	}
	return nil
}
```

`has_unread` was never in the clause and must NEVER be added.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/ -v`
Expected: ALL PASS. The clobber test now passes; pre-existing `TestUpsertAndGetChannel` may still pass (it only checks insert path).

- [ ] **Step 5: Commit**

```bash
git add internal/cache/channels.go internal/cache/channels_read_state_test.go
git commit -m "cache: stop UpsertChannel from clobbering read state on conflict"
```

---

## Phase 2: Migrate write paths to the new API

### Task 7: Migrate `OnChannelMarked` event handler

**Files:**
- Modify: `cmd/slk/main.go:2876-2898`
- Create: `cmd/slk/event_handler_marked_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/slk/event_handler_marked_test.go`:

```go
package main

import (
	"testing"

	"github.com/gammons/slk/internal/cache"
)

func TestOnChannelMarked_WritesReadState(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	// Pre-seed unread to verify the marked event clears it.
	if err := db.UpdateChannelReadState("C1", "1.0000", true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	wctx := &WorkspaceContext{}
	h := &rtmEventHandler{
		db:       db,
		wsCtx:    wctx,
		isActive: func() bool { return true },
		program:  nil, // exercise the no-program path
	}

	h.OnChannelMarked("C1", "1.0050", 0)

	state, err := db.GetChannelReadState("C1")
	if err != nil {
		t.Fatalf("GetChannelReadState: %v", err)
	}
	if state.LastReadTS != "1.0050" {
		t.Errorf("LastReadTS = %q, want %q", state.LastReadTS, "1.0050")
	}
	if state.HasUnread {
		t.Errorf("HasUnread should be false after channel_marked")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/slk/ -run TestOnChannelMarked_WritesReadState -v`
Expected: FAIL — current handler only updates `last_read_ts`, never sets `has_unread=false`.

- [ ] **Step 3: Update `OnChannelMarked`**

Edit `cmd/slk/main.go:2876-2898`:

```go
func (h *rtmEventHandler) OnChannelMarked(channelID, ts string, unreadCount int) {
	if err := h.db.UpdateChannelReadState(channelID, ts, false); err != nil {
		log.Printf("Warning: failed to update read state on channel_marked %s/%s: %v", channelID, ts, err)
	}
	if h.isActive != nil && !h.isActive() {
		return
	}
	if h.program == nil {
		return
	}
	h.program.Send(ui.ChannelMarkedRemoteMsg{
		ChannelID:   channelID,
		TS:          ts,
		UnreadCount: unreadCount,
	})
}
```

Note: removed the `h.wsCtx.LastReadMap[channelID] = ts` write. We will purge `LastReadMap` entirely in a later task.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/slk/ -run TestOnChannelMarked_WritesReadState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/slk/main.go cmd/slk/event_handler_marked_test.go
git commit -m "cmd/slk: route OnChannelMarked through UpdateChannelReadState"
```

---

### Task 8: Migrate `markChannelReadAsync`

**Files:**
- Modify: `cmd/slk/main.go:1997-2020`

- [ ] **Step 1: Add a test**

Append to `cmd/slk/event_handler_marked_test.go`:

```go
import "context"

type fakeReader struct{ called bool; ts string }
func (f *fakeReader) MarkChannel(ctx context.Context, channelID, ts string) error {
	f.called = true
	f.ts = ts
	return nil
}

// markChannelReadAsync calls client.MarkChannel via WorkspaceContext.Client which is
// the real slackclient.Client; an integration-style test is heavy. Instead, exercise
// the DB write path through a sibling helper if the production fn refuses extraction.
// For this task we just assert the DB row after calling the function with a stubbed client.
// The simplest way is to extract markChannelReadAsync into a form that accepts a small
// interface; if extracting is too risky, defer this test and rely on Task 7's coverage.
```

(If extraction is too invasive, skip the unit test for this task and rely on integration-level coverage from the reconnect-backfill scenario test in Task 14.)

- [ ] **Step 2: Edit `markChannelReadAsync`**

Edit `cmd/slk/main.go:1997-2020`:

```go
func markChannelReadAsync(
	ctx context.Context,
	wctx *WorkspaceContext,
	db *cache.DB,
	p *tea.Program,
	channelID, ts string,
) {
	if wctx == nil || ts == "" {
		return
	}
	client := wctx.Client
	go func() {
		_ = client.MarkChannel(ctx, channelID, ts)
		if err := db.UpdateChannelReadState(channelID, ts, false); err != nil {
			log.Printf("Warning: failed to update read state in markChannelReadAsync %s/%s: %v", channelID, ts, err)
		}
		if p != nil {
			p.Send(ui.ChannelMarkedReadMsg{ChannelID: channelID})
		}
	}()
}
```

Removed: `_ = db.UpdateLastReadTS(channelID, ts)`, `lastReadMap[channelID] = ts`, and the `lastReadMap := wctx.LastReadMap` capture.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/slk/`
Expected: success.

- [ ] **Step 4: Run all package tests**

Run: `go test ./cmd/slk/ -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/slk/main.go
git commit -m "cmd/slk: route markChannelReadAsync through UpdateChannelReadState"
```

---

### Task 9: Migrate `SetMessageMarkUnreader` (user presses U)

**Files:**
- Modify: `cmd/slk/main.go:958-999`

- [ ] **Step 1: Locate the existing callback**

Read the existing `app.SetMessageMarkUnreader(func(...) tea.Msg {...})` registration at approximately `cmd/slk/main.go:958-999`. Identify the block that today does:

```go
err = client.MarkChannelUnread(ctx, channelID, boundaryTS)
if err == nil {
    if dbErr := db.UpdateLastReadTS(channelID, boundaryTS); dbErr != nil {
        log.Printf("Warning: ...")
    }
    lastReadMap[channelID] = boundaryTS
}
```

- [ ] **Step 2: Replace with the new API**

Change the `if threadTS == ""` branch body to:

```go
err = client.MarkChannelUnread(ctx, channelID, boundaryTS)
if err == nil {
    if dbErr := db.UpdateChannelReadState(channelID, boundaryTS, true); dbErr != nil {
        log.Printf("Warning: failed to update read state on mark-unread %s/%s: %v", channelID, boundaryTS, dbErr)
    }
}
```

Remove the `lastReadMap := wctx.LastReadMap` capture earlier in the closure (and any other `lastReadMap[...]` writes inside).

- [ ] **Step 3: Verify build + tests**

Run: `go build ./cmd/slk/ && go test ./cmd/slk/ -v`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "cmd/slk: route mark-unread through UpdateChannelReadState"
```

---

### Task 10: Migrate `OnMessage` event handler (with active-channel suppression)

**Files:**
- Modify: `cmd/slk/main.go:2542-2681` (the `OnMessage` body)
- Modify: `cmd/slk/event_handler_test.go`

The current handler has two branches:
- Inactive workspace: bumps `wctx.Channels[i].UnreadCount` and sends `WorkspaceUnreadMsg`.
- Active workspace: dispatches `ui.NewMessageMsg`; the bump happens in `App.Update` via `a.sidebar.MarkUnread(msg.ChannelID)` after checking `msg.ChannelID != a.activeChannelID`.

After this task, BOTH branches converge: the handler writes `UpdateChannelReadState(id, "", true)` whenever the message channel is NOT the currently-active channel. We move active-channel suppression directly into `OnMessage`.

- [ ] **Step 1: Add an `activeChannelID` accessor to the handler**

The handler already has `isActive func() bool` for active-workspace. We need a similar callback to compare against the active channel ID. Edit the `rtmEventHandler` struct (search `cmd/slk/main.go` for `type rtmEventHandler struct`) to add:

```go
type rtmEventHandler struct {
    // ... existing fields ...
    activeChannelID func() string // returns "" if no channel active or workspace inactive
}
```

Find where `rtmEventHandler` is constructed (`cmd/slk/main.go`, search for `rtmEventHandler{`) and wire a closure that returns the current active channel id from the App via `router.Active()` + an exported app accessor, OR via a shared atomic variable. The simplest pattern is to follow the existing `isActive func() bool` wiring (see how that's set near construction) and add `activeChannelID` alongside it.

If wiring through the App is awkward, define a simple shared variable:

```go
// In main.go near other shared state:
var globalActiveChannelID atomic.Value // string

// In wherever channel switches are processed (search for ChannelSwitchedMsg or
// the function that sets the active channel), set:
globalActiveChannelID.Store(channelID)

// On workspace switch / channel close, set "":
globalActiveChannelID.Store("")

// Construct the handler with:
activeChannelID: func() string {
    v, _ := globalActiveChannelID.Load().(string)
    return v
},
```

Prefer the existing isActive pattern unless that requires deeper plumbing.

- [ ] **Step 2: Write the failing tests**

Append to `cmd/slk/event_handler_test.go`:

```go
func TestOnMessage_InactiveChannel_SetsHasUnread(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	h := &rtmEventHandler{
		db:              db,
		wsCtx:           &WorkspaceContext{},
		isActive:        func() bool { return true },
		activeChannelID: func() string { return "C2" }, // viewing a different channel
	}
	h.OnMessage("C1", "U1", "1.001", "hi", "", "", false, nil, slack.Blocks{}, nil)

	s, _ := db.GetChannelReadState("C1")
	if !s.HasUnread {
		t.Errorf("HasUnread = false, want true for inactive-channel message")
	}
}

func TestOnMessage_ActiveChannel_DoesNotSetHasUnread(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	h := &rtmEventHandler{
		db:              db,
		wsCtx:           &WorkspaceContext{},
		isActive:        func() bool { return true },
		activeChannelID: func() string { return "C1" }, // viewing the same channel
	}
	h.OnMessage("C1", "U1", "1.001", "hi", "", "", false, nil, slack.Blocks{}, nil)

	s, _ := db.GetChannelReadState("C1")
	if s.HasUnread {
		t.Errorf("HasUnread = true, want false (active-channel suppression)")
	}
}

func TestOnMessage_InactiveWorkspace_StillSetsHasUnread(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	h := &rtmEventHandler{
		db:              db,
		wsCtx:           &WorkspaceContext{},
		isActive:        func() bool { return false },
		activeChannelID: func() string { return "" },
	}
	h.OnMessage("C1", "U1", "1.001", "hi", "", "", false, nil, slack.Blocks{}, nil)

	s, _ := db.GetChannelReadState("C1")
	if !s.HasUnread {
		t.Errorf("HasUnread = false, want true (inactive workspace)")
	}
}
```

You may need to add imports: `"github.com/slack-go/slack"` and `"github.com/gammons/slk/internal/cache"` if missing. Check the existing event_handler_test.go imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd/slk/ -run TestOnMessage -v`
Expected: FAIL.

- [ ] **Step 4: Add the read-state write to OnMessage**

Inside `OnMessage`, near the top after early-return guards but before any of the existing notification/caching work, add:

```go
// Read-state: any message in a channel the user is NOT actively viewing
// becomes an unread. Active-channel suppression and the markChannelReadAsync
// flow handle clearing.
if h.activeChannelID == nil || h.activeChannelID() != channelID {
    if err := h.db.UpdateChannelReadState(channelID, "", true); err != nil {
        log.Printf("Warning: failed to set has_unread for %s: %v", channelID, err)
    }
}
```

Then DELETE the old inactive-workspace bump block (the one that mutates `wctx.Channels[i].UnreadCount` and sends `WorkspaceUnreadMsg` — keep the `WorkspaceUnreadMsg` send for now; we'll rip it in Task 17). For now, keep `WorkspaceUnreadMsg` so the rail still updates; we replace it later.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/slk/ -run TestOnMessage -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/slk/main.go cmd/slk/event_handler_test.go
git commit -m "cmd/slk: route OnMessage through UpdateChannelReadState with active-channel suppression"
```

---

### Task 11: Migrate bootstrap to `BatchUpdateChannelReadState`

**Files:**
- Modify: `cmd/slk/main.go:1597-1620` (the `connectWorkspace` block that populates from `GetUnreadCounts`)

- [ ] **Step 1: Add test (bootstrap is hard to unit-test in isolation; rely on integration coverage)**

The bootstrap flow runs inside `connectWorkspace` and depends on a live Slack client. Skip writing a new unit test for this task; coverage comes from the reconnect-backfill scenario test in Task 14 (the bootstrap and reconnect-catchup write paths share the same DB code path).

- [ ] **Step 2: Replace the population block**

Find the block at `cmd/slk/main.go:1597-1620`:

```go
unreadCounts, threadsAgg, ucErr := client.GetUnreadCounts()
// ... existing handling of ucErr / threadsAgg ...
unreadMap := make(map[string]int)
for _, u := range unreadCounts {
    if u.HasUnread {
        unreadMap[u.ChannelID] = u.Count
    }
    if u.LastRead != "" {
        wctx.LastReadMap[u.ChannelID] = u.LastRead
        _ = db.UpdateLastReadTS(u.ChannelID, u.LastRead)
    }
}
for i := range wctx.Channels {
    if count, ok := unreadMap[wctx.Channels[i].ID]; ok {
        wctx.Channels[i].UnreadCount = count
    }
    if lr, ok := wctx.LastReadMap[wctx.Channels[i].ID]; ok {
        wctx.Channels[i].LastReadTS = lr
    }
}
```

Replace with:

```go
unreadCounts, threadsAgg, ucErr := client.GetUnreadCounts()
// ... existing handling of ucErr / threadsAgg unchanged ...
if ucErr == nil {
    updates := make([]cache.ChannelReadStateUpdate, 0, len(unreadCounts))
    for _, u := range unreadCounts {
        updates = append(updates, cache.ChannelReadStateUpdate{
            ChannelID:  u.ChannelID,
            LastReadTS: u.LastRead, // may be ""; new API preserves existing in that case
            HasUnread:  u.HasUnread,
        })
    }
    if err := db.BatchUpdateChannelReadState(updates); err != nil {
        log.Printf("Warning: bootstrap BatchUpdateChannelReadState: %v", err)
    }
}
```

Remove the in-memory `unreadMap` population and the `for i := range wctx.Channels` propagation entirely.

- [ ] **Step 3: Verify build and tests**

Run: `go build ./cmd/slk/ && go test ./cmd/slk/ -v`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "cmd/slk: bootstrap writes read state via BatchUpdateChannelReadState"
```

---

### Task 12: Fix `runChannelPhase` to write read state on reconnect catch-up

**Files:**
- Modify: `cmd/slk/reconnect_backfill.go:99-158` (the `runChannelPhase` function)
- Modify: `cmd/slk/reconnect_backfill_test.go`

This is the fix for Symptom 2: "channels show as unread in slk after the user has read them in the official client."

- [ ] **Step 1: Write the failing test**

Append to `cmd/slk/reconnect_backfill_test.go`:

```go
func TestRunChannelPhase_WritesReadStateForAllChannels(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "a", Type: "channel"})
	_ = db.UpsertChannel(cache.Channel{ID: "C2", WorkspaceID: "T1", Name: "b", Type: "channel"})
	_ = db.UpsertChannel(cache.Channel{ID: "C3", WorkspaceID: "T1", Name: "c", Type: "channel"})

	// Pre-seed C2 as unread, mirroring "user had unreads pre-suspend".
	_ = db.UpdateChannelReadState("C2", "1.0000", true)

	fh := &fakeHistory{
		unreads: []slackclient.UnreadInfo{
			{ChannelID: "C1", HasUnread: true, LastRead: "1.0010"},
			{ChannelID: "C2", HasUnread: false, LastRead: "1.0020"}, // user read it in official client
			{ChannelID: "C3", HasUnread: false, LastRead: "1.0030"},
		},
	}
	b := newBackfillerForTest(t, db, fh, "T1")
	if err := b.run(context.Background()); err != nil {
		t.Fatalf("backfill run: %v", err)
	}

	s1, _ := db.GetChannelReadState("C1")
	if !s1.HasUnread || s1.LastReadTS != "1.0010" {
		t.Errorf("C1 = %+v", s1)
	}
	s2, _ := db.GetChannelReadState("C2")
	if s2.HasUnread {
		t.Errorf("C2 HasUnread still true after catch-up (Symptom 2 not fixed): %+v", s2)
	}
	if s2.LastReadTS != "1.0020" {
		t.Errorf("C2 LastReadTS = %q, want %q", s2.LastReadTS, "1.0020")
	}
	s3, _ := db.GetChannelReadState("C3")
	if s3.HasUnread || s3.LastReadTS != "1.0030" {
		t.Errorf("C3 = %+v", s3)
	}
}
```

The helper `newBackfillerForTest` should mirror the existing test-only constructor pattern in this file. If one doesn't exist, write a thin wrapper that mirrors how `TestBackfill_OvernightSuspendScenario` constructs its backfiller (read the test file to find the exact pattern).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/slk/ -run TestRunChannelPhase_WritesReadStateForAllChannels -v`
Expected: FAIL — C2 still `HasUnread=true`.

- [ ] **Step 3: Update `runChannelPhase`**

Edit `cmd/slk/reconnect_backfill.go:99-158`. Replace the block at lines 113-121:

```go
var unreadIDs []string
if unreads, _, err := b.client.GetUnreadCounts(); err != nil {
    debuglog.Backfill("team=%s GetUnreadCounts err=%v (falling back to cached-only)", b.workspaceID, err)
} else {
    for _, u := range unreads {
        if u.HasUnread {
            unreadIDs = append(unreadIDs, u.ChannelID)
        }
    }
}
```

with:

```go
var unreadIDs []string
if unreads, _, err := b.client.GetUnreadCounts(); err != nil {
    debuglog.Backfill("team=%s GetUnreadCounts err=%v (falling back to cached-only)", b.workspaceID, err)
} else {
    updates := make([]cache.ChannelReadStateUpdate, 0, len(unreads))
    for _, u := range unreads {
        updates = append(updates, cache.ChannelReadStateUpdate{
            ChannelID:  u.ChannelID,
            LastReadTS: u.LastRead,
            HasUnread:  u.HasUnread,
        })
        if u.HasUnread {
            unreadIDs = append(unreadIDs, u.ChannelID)
        }
    }
    if err := b.db.BatchUpdateChannelReadState(updates); err != nil {
        debuglog.Backfill("team=%s BatchUpdateChannelReadState err=%v", b.workspaceID, err)
    }
}
```

The `unreadIDs` list is still used downstream to broaden the backfill set; only the read-state write is added.

Make sure `cache` is imported in this file (`github.com/gammons/slk/internal/cache`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/slk/ -run TestRunChannelPhase_WritesReadStateForAllChannels -v`
Expected: PASS.

- [ ] **Step 5: Run the full reconnect test suite**

Run: `go test ./cmd/slk/ -run TestBackfill -v`
Expected: existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/slk/reconnect_backfill.go cmd/slk/reconnect_backfill_test.go
git commit -m "cmd/slk: reconnect catch-up writes read state for all channels in GetUnreadCounts response"
```

---

## Phase 3: UI consumers — sidebar reads from DB

### Task 13: Add `ReadStateChangedMsg` and `ReadStateReader` callback

**Files:**
- Modify: `internal/ui/app.go` (add message type near other Msg definitions)
- Modify: `internal/ui/sidebar/model.go` (add reader callback field + setter)

- [ ] **Step 1: Add the message type**

In `internal/ui/app.go`, find the cluster of Msg type definitions near lines 200-250 (where `ChannelMarkedReadMsg`, `WorkspaceUnreadMsg`, etc. live). Add:

```go
// ReadStateChangedMsg is sent whenever the persistent read state changes,
// to force panels that read from cache.GetWorkspaceReadState to re-render.
// ChannelID may be "" if the change is multi-channel (e.g., batch update
// from reconnect catch-up).
type ReadStateChangedMsg struct {
    WorkspaceID string
    ChannelID   string
}
```

- [ ] **Step 2: Add the reader callback to sidebar**

In `internal/ui/sidebar/model.go`, find the `Model` struct (search for `type Model struct`). Add a new field:

```go
type Model struct {
    // ... existing fields ...

    // readStateReader returns the per-channel read state map for the
    // active workspace, keyed by channel ID. May be nil during early
    // construction; nil means "treat everything as no-unread."
    readStateReader func() map[string]cache.ReadState
}
```

Add import `"github.com/gammons/slk/internal/cache"` if missing.

Add a setter:

```go
// SetReadStateReader installs a callback the sidebar calls during View()
// to fetch per-channel read state. Must be set before the first render
// for unread dots to appear.
func (m *Model) SetReadStateReader(f func() map[string]cache.ReadState) {
    m.readStateReader = f
    m.invalidateCache()
}
```

Use whatever the existing cache-invalidate helper is — check for `dirty()` or `invalidateCache()` or `m.cacheValid = false`. Match the existing pattern.

- [ ] **Step 3: Wire the reader from main.go**

In `cmd/slk/main.go`, find where `app.SetSectionsProvider(...)` is called (search for `SetSectionsProvider`) and add nearby:

```go
app.SetReadStateReader(func() map[string]cache.ReadState {
    wctx := router.Active()
    if wctx == nil {
        return nil
    }
    state, err := db.GetWorkspaceReadState(wctx.WorkspaceID)
    if err != nil {
        log.Printf("Warning: GetWorkspaceReadState: %v", err)
        return nil
    }
    return state
})
```

And in `internal/ui/app.go`, add the `SetReadStateReader` method that forwards to the sidebar:

```go
func (a *App) SetReadStateReader(f func() map[string]cache.ReadState) {
    a.readStateReader = f
    a.sidebar.SetReadStateReader(f)
}
```

with a corresponding `readStateReader` field on the `App` struct.

- [ ] **Step 4: Handle `ReadStateChangedMsg` in `App.Update`**

In `App.Update`, add a case alongside the other read-state-related Msg cases:

```go
case ReadStateChangedMsg:
    a.sidebar.invalidateCache() // or m.dirty() — whatever the sidebar exposes
    return a, nil
```

You may need to expose a public `Invalidate()` method on the sidebar Model if `invalidateCache` is unexported.

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/app.go internal/ui/sidebar/model.go cmd/slk/main.go
git commit -m "ui: add ReadStateChangedMsg and SetReadStateReader plumbing"
```

---

### Task 14: Sidebar `View()` reads from DB; delete `MarkUnread`/`ClearUnread`/`SetUnreadCount`

**Files:**
- Modify: `internal/ui/sidebar/model.go`
- Modify: `internal/ui/sidebar/model_test.go` (delete or update tests)
- Modify: `internal/ui/app.go` (delete the now-broken callers)

- [ ] **Step 1: Capture read state at render time**

In `internal/ui/sidebar/model.go`, find `View(height, width int) string` and `buildCache(width int)` (or whatever the row-rendering loop is called). Wherever the loop iterates `m.items` to produce row strings, add at the top of the function:

```go
var readState map[string]cache.ReadState
if m.readStateReader != nil {
    readState = m.readStateReader()
}
```

Then everywhere the code currently checks `item.UnreadCount > 0`, change to:

```go
state := readState[item.ID]
hasUnread := state.HasUnread
```

And use `hasUnread` in:
- `model.go:1146-1154` (unread dot rendering)
- `model.go:1230-1254` (bold/style switching)
- `model.go:1156-1182` (prefix selection if it depends on unread)

- [ ] **Step 2: Update `aggregateUnreadForSection`**

Edit `internal/ui/sidebar/model.go:967-980`:

```go
func (m *Model) aggregateUnreadForSection(section string) int {
    var readState map[string]cache.ReadState
    if m.readStateReader != nil {
        readState = m.readStateReader()
    }
    total := 0
    for _, idx := range m.filtered {
        item := m.items[idx]
        if item.IsMuted {
            continue
        }
        if m.sectionFor(item) != section {
            continue
        }
        if readState[item.ID].HasUnread {
            total++ // count channels-with-unreads, not sum-of-counts
        }
    }
    return total
}
```

- [ ] **Step 3: Delete `MarkUnread`, `ClearUnread`, `SetUnreadCount`**

Delete the three functions in `internal/ui/sidebar/model.go`:
- `MarkUnread` (lines ~714-733)
- `ClearUnread` (lines ~735-750)
- `SetUnreadCount` (lines ~752-778)

- [ ] **Step 4: Update App.Update to stop calling them**

In `internal/ui/app.go`:

- Line ~1781 (`case NewMessageMsg:` → `a.sidebar.MarkUnread(msg.ChannelID)`): delete this call. The DB write happens in the WS handler now. After the line, send `ReadStateChangedMsg`:

  ```go
  case NewMessageMsg:
      // (existing handling minus the MarkUnread call)
      return a, func() tea.Msg { return ReadStateChangedMsg{ChannelID: msg.ChannelID} }
  ```

- Line ~2091 (`case ChannelMarkedReadMsg:` → `a.sidebar.ClearUnread(msg.ChannelID)`): replace with `a.sidebar.Invalidate()` (or send `ReadStateChangedMsg`).

- Lines ~1876-1890 + ~5549 (`applyChannelMark` + `case MessageMarkedUnreadMsg:` + `case ChannelMarkedRemoteMsg:`): the `SetUnreadCount` call inside `applyChannelMark` goes away. The DB write happened in the WS handler (`OnChannelMarked` / mark-unread callback); the App just needs to invalidate the sidebar cache. Replace `a.sidebar.SetUnreadCount(channelID, unreadCount)` with `a.sidebar.Invalidate()`.

If `Invalidate()` isn't yet exported, add to `internal/ui/sidebar/model.go`:

```go
// Invalidate forces the next View() call to re-read read state from the
// installed reader, bypassing any internal caching.
func (m *Model) Invalidate() {
    m.dirty() // or whatever the existing helper is
}
```

- [ ] **Step 5: Delete sidebar-model tests for removed methods**

In `internal/ui/sidebar/model_test.go`, delete:
- `TestMarkUnread_IncrementsCount`
- `TestMarkUnread_BumpsVersionAndInvalidatesCache`
- `TestMarkUnread_UnknownChannelIsNoop`
- `TestMarkUnread_RendersDotAndBold`
- `TestSetUnreadCount_SetsExactValue`
- `TestSetUnreadCount_Zero_ClearsBadge`
- `TestSetUnreadCount_UnknownChannel_NoOp`
- `TestUpsertItem_ThenMarkUnread`

These exercise methods that no longer exist.

- [ ] **Step 6: Build and run tests**

Run: `go build ./... && go test ./internal/ui/sidebar/ -v`
Expected: build succeeds, sidebar tests pass.

Run: `go test ./internal/ui/ -v`
Expected: App tests pass (some that exercised the old paths may need updates — fix as needed by adjusting assertions to check the DB state rather than `sidebar.items[i].UnreadCount`).

- [ ] **Step 7: Add a new sidebar-rendering test driven by DB state**

Append to `internal/ui/sidebar/model_test.go`:

```go
func TestView_RendersDotFromReadStateReader(t *testing.T) {
    m := New()
    m.SetItems([]ChannelItem{
        {ID: "C1", Name: "general", Type: "channel", Section: "channels"},
        {ID: "C2", Name: "random", Type: "channel", Section: "channels"},
    })
    state := map[string]cache.ReadState{
        "C1": {HasUnread: true},
        "C2": {HasUnread: false},
    }
    m.SetReadStateReader(func() map[string]cache.ReadState { return state })

    out := m.View(20, 30)
    if !strings.Contains(out, "general") {
        t.Fatalf("missing channel name; got:\n%s", out)
    }
    // The actual dot character depends on theme; assert that C1's row
    // differs from C2's row in the dot prefix area. The cleanest check
    // is to count occurrences of the dot rune used by the sidebar.
    // Replace `unreadDotStr` below with the actual exported symbol if available.
    dotCount := strings.Count(out, unreadDotStr)
    if dotCount != 1 {
        t.Errorf("expected exactly 1 unread dot, got %d. Output:\n%s", dotCount, out)
    }
}

func TestView_MutedChannelNoDot(t *testing.T) {
    m := New()
    m.SetItems([]ChannelItem{
        {ID: "C1", Name: "noisy", Type: "channel", Section: "channels", IsMuted: true},
    })
    state := map[string]cache.ReadState{"C1": {HasUnread: true}}
    m.SetReadStateReader(func() map[string]cache.ReadState { return state })

    out := m.View(20, 30)
    if strings.Count(out, unreadDotStr) != 0 {
        t.Errorf("muted channel should not show a dot. Output:\n%s", out)
    }
}
```

Adjust constructor names (`New()`, `SetItems`) and the dot constant to match what the sidebar package actually exposes — read the file to confirm.

- [ ] **Step 8: Run new tests**

Run: `go test ./internal/ui/sidebar/ -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/ui/sidebar/model.go internal/ui/sidebar/model_test.go internal/ui/app.go
git commit -m "ui: sidebar reads unread state from cache via reader callback"
```

---

### Task 15: Send `ReadStateChangedMsg` from WS handlers

The DB writes happen in `OnMessage`, `OnChannelMarked`, `markChannelReadAsync`, and the mark-unread callback. The sidebar will not re-render unless something pushes a tea.Msg through the program. We need each write to be paired with a `ReadStateChangedMsg` send.

**Files:**
- Modify: `cmd/slk/main.go` (the four write sites)

- [ ] **Step 1: Add the send after each DB write**

In `cmd/slk/main.go`:

1. After the `UpdateChannelReadState` in `OnChannelMarked` (Task 7), before the `isActive`/program early returns:

   ```go
   if h.program != nil {
       h.program.Send(ui.ReadStateChangedMsg{ChannelID: channelID})
   }
   ```

   Keep the existing `ChannelMarkedRemoteMsg` send too — it carries the unreadCount for the toast.

2. After the `UpdateChannelReadState` in `markChannelReadAsync` (Task 8), inside the goroutine:

   ```go
   if p != nil {
       p.Send(ui.ChannelMarkedReadMsg{ChannelID: channelID})
       p.Send(ui.ReadStateChangedMsg{ChannelID: channelID})
   }
   ```

3. After the `UpdateChannelReadState` in the mark-unread callback (Task 9), the callback returns a `tea.Msg`. Have it return `ReadStateChangedMsg` chained with the existing `MessageMarkedUnreadMsg`. The simplest approach: emit the changed-msg by calling `app.program.Send(...)` if accessible, OR return a `tea.Batch(ReadStateChangedMsg{...}, MessageMarkedUnreadMsg{...})`. Use `tea.Batch` if the callback signature is `tea.Msg`; if so, switch to returning `tea.Cmd` by wrapping the existing return in a function. Easier: have App.Update for `MessageMarkedUnreadMsg` itself emit a follow-up `ReadStateChangedMsg`.

   Pick the cleanest path: have `App.Update` for the `case MessageMarkedUnreadMsg:` arm (in `internal/ui/app.go:1876-1890`) return:

   ```go
   case MessageMarkedUnreadMsg:
       // (existing handling)
       return a, func() tea.Msg { return ReadStateChangedMsg{ChannelID: msg.ChannelID} }
   ```

4. After the `UpdateChannelReadState` in `OnMessage` (Task 10), the handler runs synchronously inside the WS dispatch goroutine. Add:

   ```go
   if h.program != nil {
       h.program.Send(ui.ReadStateChangedMsg{ChannelID: channelID})
   }
   ```

   Place it after the read-state write, regardless of the active-channel branch (so the sidebar gets a chance to clear stale state too).

- [ ] **Step 2: Build and test**

Run: `go build ./... && go test ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add cmd/slk/main.go internal/ui/app.go
git commit -m "cmd/slk: emit ReadStateChangedMsg whenever read state is written"
```

---

### Task 16: Wire the workspace rail to `WorkspacesWithUnreads`

**Files:**
- Modify: `internal/ui/workspace/model.go`
- Modify: `internal/ui/app.go`
- Modify: `cmd/slk/main.go`

The current rail uses `Model.SetUnread(teamID, hasUnread bool)` driven by `WorkspaceUnreadMsg` (which only fires on inactive-workspace new messages and never clears). After this task, the rail polls `db.WorkspacesWithUnreads()` on a `ReadStateChangedMsg` and refreshes its internal `HasUnread` flags.

- [ ] **Step 1: Add a reader callback to the rail**

In `internal/ui/workspace/model.go`, mirror the sidebar pattern:

```go
type Model struct {
    // ... existing fields ...
    unreadReader func() []string // returns workspace IDs with unreads
}

func (m *Model) SetUnreadReader(f func() []string) {
    m.unreadReader = f
}

// RefreshUnreads pulls the latest set from the reader and updates each item.
// Called by the App on ReadStateChangedMsg.
func (m *Model) RefreshUnreads() {
    if m.unreadReader == nil {
        return
    }
    set := make(map[string]bool, len(m.items))
    for _, id := range m.unreadReader() {
        set[id] = true
    }
    for i := range m.items {
        m.items[i].HasUnread = set[m.items[i].ID]
    }
}
```

- [ ] **Step 2: Wire the reader from main.go**

In `cmd/slk/main.go`, near where `SetReadStateReader` was wired (Task 13):

```go
app.SetWorkspaceUnreadReader(func() []string {
    ids, err := db.WorkspacesWithUnreads()
    if err != nil {
        log.Printf("Warning: WorkspacesWithUnreads: %v", err)
        return nil
    }
    return ids
})
```

In `internal/ui/app.go`:

```go
func (a *App) SetWorkspaceUnreadReader(f func() []string) {
    a.workspaceRail.SetUnreadReader(f)
}
```

- [ ] **Step 3: Refresh on ReadStateChangedMsg**

In `internal/ui/app.go`, update the `case ReadStateChangedMsg:` arm to also refresh the rail:

```go
case ReadStateChangedMsg:
    a.sidebar.Invalidate()
    a.workspaceRail.RefreshUnreads()
    return a, nil
```

- [ ] **Step 4: Stop sending `WorkspaceUnreadMsg`**

The current `OnMessage` in `cmd/slk/main.go:2647-2652` sends `WorkspaceUnreadMsg` for the inactive-workspace case. Delete that send. The rail now refreshes whenever a `ReadStateChangedMsg` fires (Task 15 already routes that from `OnMessage`).

Also delete the `case WorkspaceUnreadMsg:` arm in `App.Update` (`internal/ui/app.go:2207-2208`) and the `WorkspaceUnreadMsg` struct definition (`app.go:240-243`). Search for any other senders to be sure they're all removed.

- [ ] **Step 5: Build and test**

Run: `go build ./... && go test ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/workspace/model.go internal/ui/app.go cmd/slk/main.go
git commit -m "ui: workspace rail unread dot driven by db.WorkspacesWithUnreads"
```

---

## Phase 4: Remove the in-memory stores

### Task 17: Remove `WorkspaceContext.LastReadMap`

**Files:**
- Modify: `cmd/slk/main.go` (multiple sites)

Every reader of `wctx.LastReadMap` migrates to `db.GetChannelReadState(channelID).LastReadTS`.

Per the exploration, the use sites are:

- `main.go:150` — field declaration
- `main.go:817` — `app.SetLastReadProvider(...)` closure
- `main.go:875` — `lastReadTS := wctx.LastReadMap[channelID]` for `MessagesLoadedMsg`
- `main.go:964` — inside `SetMessageMarkUnreader` (already handled in Task 9)
- `main.go:1416` — init
- `main.go:1609` — bootstrap write (already handled in Task 11)
- `main.go:1617` — bootstrap propagate
- `main.go:2011` — `markChannelReadAsync` (already handled in Task 8)
- `main.go:2882-2883` — `OnChannelMarked` (already handled in Task 7)

- [ ] **Step 1: Replace each read site with a DB call**

For each remaining reader, change:

```go
lastReadTS := wctx.LastReadMap[channelID]
```

to:

```go
state, _ := db.GetChannelReadState(channelID)
lastReadTS := state.LastReadTS
```

For the `app.SetLastReadProvider` closure at line 817:

```go
app.SetLastReadProvider(func(channelID string) string {
    state, err := db.GetChannelReadState(channelID)
    if err != nil {
        return ""
    }
    return state.LastReadTS
})
```

- [ ] **Step 2: Delete the field, init, and any remaining writers**

Remove the `LastReadMap` field from the `WorkspaceContext` struct at line 150. Remove its initialization at line 1416 and anywhere else `LastReadMap = make(map[string]string)` appears. Search for any remaining references:

```bash
git grep -n LastReadMap
```

Every result must be deleted or migrated.

- [ ] **Step 3: Build and test**

Run: `go build ./... && go test ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "cmd/slk: remove WorkspaceContext.LastReadMap (use cache.GetChannelReadState)"
```

---

### Task 18: Remove `LastReadTS` and `UnreadCount` from `sidebar.ChannelItem`

**Files:**
- Modify: `internal/ui/sidebar/model.go:28-49` (the `ChannelItem` struct)
- Modify: every site that constructs or reads `ChannelItem.LastReadTS` or `.UnreadCount`

- [ ] **Step 1: Delete the fields**

Edit the struct:

```go
type ChannelItem struct {
    ID           string
    Name         string
    Type         string
    Section      string
    SectionOrder int
    IsStarred    bool
    Presence     string
    DMUserID     string
    IsMuted      bool
}
```

- [ ] **Step 2: Fix every compile error**

Run: `go build ./...`

Expected: many compile failures. For each, the pattern is:
- Construction site that sets `.LastReadTS` or `.UnreadCount`: delete those field assignments.
- Read site: replace with `db.GetChannelReadState(item.ID).LastReadTS` or `.HasUnread`.

Common sites (from the exploration):
- `cmd/slk/channelitem.go` — drop any `LastReadTS:` / `UnreadCount:` from struct literals.
- `cmd/slk/main.go` — any `wctx.Channels[i].LastReadTS = ...` or `wctx.Channels[i].UnreadCount = ...` deletions.
- `internal/ui/sidebar/model.go` — any internal use (the dot-rendering paths were already swapped out in Task 14).
- `internal/ui/app.go` — `WorkspaceSwitchedMsg.Channels` consumers.

- [ ] **Step 3: Build and test**

Run: `go build ./... && go test ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "ui/sidebar: remove ChannelItem.LastReadTS and UnreadCount fields"
```

---

### Task 19: Remove `LastReadTS` and `UnreadCount` from `cache.Channel`

**Files:**
- Modify: `internal/cache/channels.go` (struct, `UpsertChannel`, `GetChannel`, `ListChannels`)
- Modify: `internal/cache/channels_test.go`
- Modify: every caller

This is the type-system enforcement called out in the spec.

- [ ] **Step 1: Delete the fields from the struct**

```go
type Channel struct {
    ID          string
    WorkspaceID string
    Name        string
    Type        string
    Topic       string
    IsMember    bool
    IsStarred   bool
    UpdatedAt   int64
}
```

- [ ] **Step 2: Update `UpsertChannel`, `GetChannel`, `ListChannels`**

`UpsertChannel`: stop selecting `last_read_ts` and `unread_count` in the INSERT (they will use schema defaults). Drop them from the param list too:

```go
func (db *DB) UpsertChannel(ch Channel) error {
    _, err := db.conn.Exec(`
        INSERT INTO channels (id, workspace_id, name, type, topic, is_member, is_starred, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            name=excluded.name,
            type=excluded.type,
            topic=excluded.topic,
            is_member=excluded.is_member,
            is_starred=excluded.is_starred,
            updated_at=excluded.updated_at
    `, ch.ID, ch.WorkspaceID, ch.Name, ch.Type, ch.Topic,
        boolToInt(ch.IsMember), boolToInt(ch.IsStarred), ch.UpdatedAt)
    if err != nil {
        return fmt.Errorf("upserting channel: %w", err)
    }
    return nil
}
```

`GetChannel` and `ListChannels`: drop `last_read_ts, unread_count` from the SELECT and the Scan target list:

```go
func (db *DB) GetChannel(id string) (Channel, error) {
    var ch Channel
    var isMember, isStarred int
    err := db.conn.QueryRow(`
        SELECT id, workspace_id, name, type, topic, is_member, is_starred, updated_at
        FROM channels WHERE id = ?
    `, id).Scan(&ch.ID, &ch.WorkspaceID, &ch.Name, &ch.Type, &ch.Topic,
        &isMember, &isStarred, &ch.UpdatedAt)
    if err != nil {
        return ch, fmt.Errorf("getting channel: %w", err)
    }
    ch.IsMember = isMember == 1
    ch.IsStarred = isStarred == 1
    return ch, nil
}
```

Same pattern for `ListChannels`.

- [ ] **Step 3: Delete the legacy helpers**

Remove from `internal/cache/channels.go`:
- `UpdateUnreadCount` (lines 91-97)
- `UpdateLastReadTS` (lines 99-109)
- `GetLastReadTS` (lines 111-122)

These have no remaining callers after Phase 2.

- [ ] **Step 4: Update `internal/cache/channels_test.go`**

Delete:
- `TestUpdateUnreadCount`
- `TestUpdateLastReadTS_RoundTrip`

Update `TestUpsertAndGetChannel` to not assert against `LastReadTS` or `UnreadCount` (just delete those assertions).

- [ ] **Step 5: Fix every other caller**

Run: `go build ./...`
Expected: compile failures in any file that referenced the removed fields. Fix each by deletion.

- [ ] **Step 6: Build and test**

Run: `go build ./... && go test ./...`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "cache: remove Channel.LastReadTS, Channel.UnreadCount, and legacy read-state helpers"
```

---

## Phase 5: Integration test (lock in Symptom 2 fix)

### Task 20: Extend `TestBackfill_OvernightSuspendScenario` with channel D

**Files:**
- Modify: `cmd/slk/reconnect_backfill_test.go:603-726`

- [ ] **Step 1: Read the existing test**

Open `cmd/slk/reconnect_backfill_test.go:603-726` (`TestBackfill_OvernightSuspendScenario`). Note the existing channel categories A, B, C and how they're set up.

- [ ] **Step 2: Add channel D to the scenario**

Add a fourth channel category to the test's setup section. Channel D semantics:

- Pre-suspend state: has unreads (`HasUnread=true`) and `last_read_ts="1.0000"`.
- During the offline window: the user read it in the official client.
- `client.counts` at reconnect returns `HasUnread=false` and `LastRead="1.0100"`.

In the test setup:

```go
// Channel D: was unread pre-suspend; user read it in the official client during the offline window.
_ = db.UpsertChannel(cache.Channel{ID: "D", WorkspaceID: "T1", Name: "d", Type: "channel"})
_ = db.UpdateChannelReadState("D", "1.0000", true) // pre-suspend snapshot
```

In the `fakeHistory.unreads`:

```go
{ChannelID: "D", HasUnread: false, LastRead: "1.0100"},
```

After `b.run(ctx)`, add assertions:

```go
d, _ := db.GetChannelReadState("D")
if d.HasUnread {
    t.Errorf("D HasUnread = true after backfill; Symptom 2 not fixed")
}
if d.LastReadTS != "1.0100" {
    t.Errorf("D LastReadTS = %q, want %q", d.LastReadTS, "1.0100")
}
```

- [ ] **Step 3: Run the test**

Run: `go test ./cmd/slk/ -run TestBackfill_OvernightSuspendScenario -v`
Expected: PASS.

- [ ] **Step 4: Run the entire test suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/slk/reconnect_backfill_test.go
git commit -m "test: lock in Symptom 2 fix with channel D in overnight-suspend scenario"
```

---

## Phase 6: Cleanup & verification

### Task 21: Verify no dead read-state state remains

**Files:**
- All

- [ ] **Step 1: Grep for any lingering references**

Run each of these. Any non-test, non-comment hit is a bug to fix:

```bash
git grep -n "LastReadMap"        # should be empty
git grep -n "UpdateUnreadCount"  # should be empty
git grep -n "UpdateLastReadTS"   # should be empty
git grep -n "GetLastReadTS"      # should be empty
git grep -n "\.UnreadCount"      # only ThreadsAggregate.UnreadCount remains (acceptable)
git grep -n "MarkUnread\|ClearUnread\|SetUnreadCount" -- 'internal/ui/sidebar/*'  # should be empty in sidebar package
git grep -n "WorkspaceUnreadMsg" # should be empty
```

For each unexpected hit, either delete it or migrate it. The `ThreadsAggregate.UnreadCount` field in `internal/slack/client.go` is unrelated (thread count) and stays.

The `slackclient.UnreadInfo.Count` field is now unused by all production callers but the field stays in place — it's deserialized from Slack's response and removing it is out of scope for this branch.

- [ ] **Step 2: Run race-detector test suite**

Run: `go test -race ./...`
Expected: PASS, no race warnings.

- [ ] **Step 3: Run `go vet` and any project linters**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 4: Manual smoke test**

Start slk against a real workspace. Verify:

1. Sidebar shows dots for channels that the official client also shows as unread.
2. Open a channel in slk → dot clears.
3. With slk closed, read a channel in the official client → reopen slk → after the reconnect catch-up settles (1-2 sec), the dot is gone.
4. With slk closed, receive a message in another workspace → reopen slk → workspace rail dot lights up.
5. Press `U` on a message → dot reappears.
6. Switch workspaces twice → dots remain correct (no stale state from in-memory stores).

- [ ] **Step 5: No-op commit if changes were needed; otherwise this task is verification only**

If anything was fixed during the grep sweep:

```bash
git add -A
git commit -m "cleanup: remove dead read-state references"
```

---

### Task 22: Write release notes / update CHANGELOG

**Files:**
- Modify: `CHANGELOG.md` (or wherever release notes live — check `git log` for the project's convention)

- [ ] **Step 1: Identify the changelog location**

```bash
ls CHANGELOG* docs/CHANGELOG* docs/changelog* 2>/dev/null
```

If none exist, skip this task; rely on the commit log.

- [ ] **Step 2: Add an entry**

```markdown
## v0.7.12 (or v0.8.0)

### Fixed
- Channels now show as unread/read in sync with the official Slack client across disconnects. Read state is consolidated into a single SQLite source of truth; reconnect catch-up pulls the latest state via `client.counts`. (Fixes #N — replace with the issue reference if any.)

### Changed
- Sidebar section aggregates ("DMs (N)", "Channels (N)") now count channels-with-unreads instead of summing per-channel unread-message counts. The Slack `unread_count_display` value was sparse and inconsistent across channel types, so the integer count is being abandoned in favor of a boolean unread signal.

### Internal
- Removed `WorkspaceContext.LastReadMap`; `sidebar.ChannelItem.LastReadTS/UnreadCount`; `cache.Channel.LastReadTS/UnreadCount`. All callers go through the new `cache.UpdateChannelReadState` / `cache.GetWorkspaceReadState` API.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "changelog: note read-state sync rewrite"
```

---

## Out of scope for this branch (genuine follow-ups)

These are deliberately deferred (mirrors the spec's "Out of scope" section):

1. Drop the now-deprecated `unread_count` column from the `channels` table.
2. `GetHistorySince` `Capped: true` on exact-`maxTotal` even when no more pages remain (v0.7.11 follow-up).
3. `GetUnreadCounts` ignores `ctx` (v0.7.11 follow-up).
4. Move watermark helpers from `channels.go` to `channels_sync.go` (cohesion improvement noted in v0.7.11 reviews).
5. Remove `slackclient.UnreadInfo.Count` field (unused after this branch).

---

## Self-review notes

**Spec coverage check (re-skim of spec sections vs. tasks):**

- "Root causes" 1 (`runChannelPhase` discards `LastRead`) → Task 12.
- "Root causes" 2 (`UpsertChannel` clobbers) → Task 6 (regression test) + Task 19 (final cleanup).
- "Root causes" 3 (`UnreadCount` set to `MentionCount`) → moot: integer count abandoned in favor of boolean. No fix needed.
- "Root causes" 4 (`UpdateUnreadCount` unused) → Task 19 deletes it.
- "Root causes" 5 (`OnChannelMarked` doesn't update `wctx.Channels[i]`) → Task 7 + Task 17 (eliminates that store entirely).
- "Root causes" 6 (`OnMessage` bypasses `wctx.Channels`) → Task 10 + Task 17.
- "Root causes" 7 (no reconnect catch-up for read state) → Task 12.
- "Single mutation API" → Tasks 3–5 (functions exist), Tasks 7–12 (all writes routed).
- "Schema changes" → Task 1 (`has_unread`); Task 19 (struct field removal); deprecated `unread_count` kept (deferred per spec).
- "Data inflows" 6 sources → Tasks 7–12.
- "Data outflows" sidebar dot, section aggregate, message pane "new messages", rail dot → Tasks 13–16.
- "Active-channel handling" → Task 10.
- "Test plan" enumerated tests → distributed across Tasks 3, 4, 5, 6, 7, 10, 12, 14, 20.

**Placeholder scan:** No "TBD", "TODO", "implement later" anywhere. Each step has either complete code or specific grep/run/edit instructions.

**Type consistency:** `ReadState{LastReadTS, HasUnread}`, `ChannelReadStateUpdate{ChannelID, LastReadTS, HasUnread}`, function names `UpdateChannelReadState`, `BatchUpdateChannelReadState`, `GetChannelReadState`, `GetWorkspaceReadState`, `WorkspacesWithUnreads` are used consistently throughout. Message type `ReadStateChangedMsg{WorkspaceID, ChannelID}` defined in Task 13 and used the same way thereafter.
