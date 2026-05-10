# Messages cache integrity and workspace state correctness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the visible "older message, then newer one second later" flicker that appears when returning to a workspace, by fixing the bootstrap race that leaves workspace callbacks bound to the wrong client, adding per-channel cache freshness tracking with a three-tier display policy, and unblocking message render on synchronous user-profile fetches.

**Architecture:** main.go owns a single `atomic.Pointer[WorkspaceContext]` (the "router"). `wireCallbacks(router)` is invoked once at startup; all workspace-scoped closures read `router.Active()` at invocation time, not at construction time. App's `ChannelSelectedMsg` handler picks one of three render strategies based on the channel's `synced_at` column. A per-workspace `userResolver` makes synchronous `users.info` calls inside message processing unnecessary; unknown authors render as their user ID, then patch live via `UserResolvedMsg`.

**Tech Stack:** Go, Bubble Tea (`charm.land/bubbletea/v2`), Lipgloss v2, modernc/sqlite, slack-go.

**Reference spec:** `docs/superpowers/specs/2026-05-10-messages-cache-integrity-design.md`.

---

## Phase 1 — Foundation primitives

These tasks are purely additive. They introduce new types, methods, and a DB column. No existing code paths are changed yet, so existing behavior is preserved through the phase.

### Task 1: Add `channels.synced_at` schema migration

**Files:**
- Modify: `internal/cache/db.go:142-152` (additive migrations block)
- Test: `internal/cache/db_test.go`

- [ ] **Step 1: Write the failing test**

Open `internal/cache/db_test.go`. Add this test at the bottom of the file (after the existing tests):

```go
func TestMigrateAddsChannelsSyncedAtColumn(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Probe PRAGMA table_info for the synced_at column on channels.
	rows, err := db.conn.Query("PRAGMA table_info(channels)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "synced_at" {
			if ctype != "INTEGER" {
				t.Errorf("synced_at type = %q, want INTEGER", ctype)
			}
			if notnull != 1 {
				t.Error("synced_at should be NOT NULL")
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("channels table missing synced_at column")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cache/ -run TestMigrateAddsChannelsSyncedAtColumn -count=1`
Expected: FAIL with "channels table missing synced_at column".

- [ ] **Step 3: Add the migration**

In `internal/cache/db.go`, locate the additive-migrations block at the end of `migrate()` (after the `addColumnIfMissing` call for `users.is_bot`). Append a new migration:

```go
	if err := db.addColumnIfMissing("channels", "synced_at",
		"ALTER TABLE channels ADD COLUMN synced_at INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cache/ -run TestMigrateAddsChannelsSyncedAtColumn -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/db.go internal/cache/db_test.go
git commit -m "cache: add channels.synced_at column for per-channel freshness"
```

---

### Task 2: Add `SetChannelSyncedAt` / `GetChannelSyncedAt` cache API

**Files:**
- Modify: `internal/cache/channels.go`
- Test: `internal/cache/channels_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/cache/channels_test.go`:

```go
func TestGetChannelSyncedAt_DefaultsToZero(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()

	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true})

	if got := db.GetChannelSyncedAt("C1"); got != 0 {
		t.Errorf("default synced_at = %d, want 0", got)
	}
}

func TestGetChannelSyncedAt_MissingChannelReturnsZero(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()

	if got := db.GetChannelSyncedAt("C-nonexistent"); got != 0 {
		t.Errorf("missing channel synced_at = %d, want 0", got)
	}
}

func TestSetChannelSyncedAt_RoundTrip(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()

	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true})

	if err := db.SetChannelSyncedAt("C1", 1700000000); err != nil {
		t.Fatal(err)
	}
	if got := db.GetChannelSyncedAt("C1"); got != 1700000000 {
		t.Errorf("synced_at = %d, want 1700000000", got)
	}

	// Overwrite.
	if err := db.SetChannelSyncedAt("C1", 1800000000); err != nil {
		t.Fatal(err)
	}
	if got := db.GetChannelSyncedAt("C1"); got != 1800000000 {
		t.Errorf("synced_at after overwrite = %d, want 1800000000", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache/ -run TestGetChannelSyncedAt -count=1 && go test ./internal/cache/ -run TestSetChannelSyncedAt -count=1`
Expected: FAIL (methods don't exist).

- [ ] **Step 3: Add the methods**

Append to `internal/cache/channels.go`:

```go
// SetChannelSyncedAt stores the unix timestamp (seconds) at which the
// channel's message cache was last authoritatively replaced from the
// network. UPSERT-style: if the channel row doesn't exist yet, this
// inserts a stub row with workspace_id="" and name=""; callers should
// have UpsertChannel'd first. The implementation uses UPDATE so that
// it only touches existing rows, avoiding the stub-row footgun.
func (db *DB) SetChannelSyncedAt(channelID string, unixSec int64) error {
	_, err := db.conn.Exec(`UPDATE channels SET synced_at = ? WHERE id = ?`, unixSec, channelID)
	if err != nil {
		return fmt.Errorf("setting channel synced_at: %w", err)
	}
	return nil
}

// GetChannelSyncedAt returns the unix timestamp recorded by
// SetChannelSyncedAt, or 0 if the channel row is missing or the column
// was never set. The zero return doubles as the "never synced" signal
// the UI layer uses to fall into the spinner-only display tier.
func (db *DB) GetChannelSyncedAt(channelID string) int64 {
	var syncedAt int64
	err := db.conn.QueryRow(`SELECT synced_at FROM channels WHERE id = ?`, channelID).Scan(&syncedAt)
	if err != nil {
		return 0
	}
	return syncedAt
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache/ -count=1`
Expected: PASS (including the new tests and all pre-existing channel tests).

- [ ] **Step 5: Commit**

```bash
git add internal/cache/channels.go internal/cache/channels_test.go
git commit -m "cache: SetChannelSyncedAt / GetChannelSyncedAt accessors"
```

---

### Task 3: Add `messages.Model.PatchUserName`

**Files:**
- Modify: `internal/ui/messages/model.go`
- Test: `internal/ui/messages/model_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/messages/model_test.go`:

```go
func TestPatchUserName_UpdatesMatchingRowsAndUserNamesMap(t *testing.T) {
	m := New()
	m.SetMessages([]MessageItem{
		{TS: "1.0", UserID: "U1", UserName: "U1", Text: "hi"},
		{TS: "2.0", UserID: "U2", UserName: "alice", Text: "hey"},
		{TS: "3.0", UserID: "U1", UserName: "U1", Text: "again"},
	})

	verBefore := m.Version()

	m.PatchUserName("U1", "bob")

	if m.messages[0].UserName != "bob" {
		t.Errorf("msg[0].UserName = %q, want bob", m.messages[0].UserName)
	}
	if m.messages[1].UserName != "alice" {
		t.Errorf("msg[1].UserName should not have changed; got %q", m.messages[1].UserName)
	}
	if m.messages[2].UserName != "bob" {
		t.Errorf("msg[2].UserName = %q, want bob", m.messages[2].UserName)
	}
	if got := m.ResolveUserName("U1"); got != "bob" {
		t.Errorf("ResolveUserName(U1) = %q, want bob", got)
	}
	if m.Version() <= verBefore {
		t.Error("Version should bump after PatchUserName")
	}
}

func TestPatchUserName_NoOpWhenUnchanged(t *testing.T) {
	m := New()
	m.SetMessages([]MessageItem{{TS: "1.0", UserID: "U1", UserName: "bob"}})
	m.PatchUserName("U1", "bob") // prime the userNames map
	verBefore := m.Version()

	m.PatchUserName("U1", "bob") // second call, identical

	if m.Version() != verBefore {
		t.Error("Version should NOT bump on no-op PatchUserName")
	}
}

func TestPatchUserName_NoMatchingMessagesStillUpdatesMap(t *testing.T) {
	m := New()
	m.SetMessages([]MessageItem{{TS: "1.0", UserID: "U1", UserName: "alice"}})

	m.PatchUserName("U99", "carol")

	if got := m.ResolveUserName("U99"); got != "carol" {
		t.Errorf("ResolveUserName(U99) = %q, want carol (mention map should update even with no message match)", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/messages/ -run TestPatchUserName -count=1`
Expected: FAIL (method doesn't exist).

- [ ] **Step 3: Add the method**

Find `SetUserNames` in `internal/ui/messages/model.go` (around line 994). Add this method directly below it:

```go
// PatchUserName updates the in-memory userNames map (used for @mention
// rendering) and overwrites the UserName field on every cached message
// authored by userID. Invalidates the render cache so the next View()
// re-renders affected rows. Idempotent: no-op when the name is
// unchanged.
//
// Used by the async user-resolution path: history fetchers stash
// MessageItem.UserName = m.UserID for unknown authors, then a
// UserResolvedMsg arrives and the App calls PatchUserName to replace
// the placeholders live without re-fetching history.
func (m *Model) PatchUserName(userID, displayName string) {
	if userID == "" {
		return
	}
	if m.userNames == nil {
		m.userNames = map[string]string{}
	}
	if m.userNames[userID] == displayName {
		return
	}
	m.userNames[userID] = displayName
	changed := false
	for i := range m.messages {
		if m.messages[i].UserID == userID && m.messages[i].UserName != displayName {
			m.messages[i].UserName = displayName
			changed = true
		}
	}
	if changed {
		m.cache = nil
	}
	m.dirty()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/messages/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/messages/model.go internal/ui/messages/model_test.go
git commit -m "messages: PatchUserName for live name updates after async resolution"
```

---

### Task 4: Add `thread.Model.PatchUserName`

**Files:**
- Modify: `internal/ui/thread/model.go`
- Test: `internal/ui/thread/model_test.go`

- [ ] **Step 1: Inspect thread.Model to find the matching insertion point**

Run: `grep -nE 'SetUserNames|userNames|MessageItem' internal/ui/thread/model.go | head -20`

The thread model stores parent + replies as a single slice (commonly named `messages` or `replies`). Find the field that holds the slice and the existing `SetUserNames`-equivalent method. The patch logic mirrors Task 3 against whichever field holds the rendered messages.

- [ ] **Step 2: Write the failing test**

Open `internal/ui/thread/model_test.go`. The test mirrors Task 3 but constructs the thread model. Use the existing test fixtures in that file as a template; the body shape:

```go
func TestThreadPatchUserName_UpdatesMatchingRows(t *testing.T) {
	m := New() // or whatever constructor thread_test.go uses

	// Seed parent + reply with userID-as-name placeholders.
	parent := messages.MessageItem{TS: "1.0", UserID: "U1", UserName: "U1", Text: "parent"}
	replies := []messages.MessageItem{
		{TS: "2.0", UserID: "U1", UserName: "U1", Text: "self-reply"},
		{TS: "3.0", UserID: "U2", UserName: "alice", Text: "alice-reply"},
	}
	m.SetThread(parent, replies, "C1", "1.0") // use the existing SetThread signature

	verBefore := m.Version()
	m.PatchUserName("U1", "bob")

	// Assertions per the existing thread.Model accessors. If the model
	// exposes `messages()` or `replies()`, assert via those. If it only
	// exposes View(), assert that rendered output contains "bob" twice
	// and "alice" once.
	if m.Version() <= verBefore {
		t.Error("Version should bump after PatchUserName")
	}
}
```

If `thread.Model` does not expose `Version()`, use `IsDirty()` or whatever the existing change-detection helper is (consult the existing `SetUserNames` tests in the same file for the pattern).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/ui/thread/ -run TestThreadPatchUserName -count=1`
Expected: FAIL (method doesn't exist).

- [ ] **Step 4: Add the method**

Add `PatchUserName` to `internal/ui/thread/model.go` next to its existing `SetUserNames` method, mirroring Task 3's implementation but walking whichever slice holds the rendered messages. Same idempotency rules: no-op if the name is unchanged; invalidate the render cache; mark dirty.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/ui/thread/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/thread/model.go internal/ui/thread/model_test.go
git commit -m "thread: PatchUserName for live name updates after async resolution"
```

---

### Task 5: Add `statusbar.Model.SetSyncing` and themable style

**Files:**
- Modify: `internal/ui/statusbar/model.go`
- Test: `internal/ui/statusbar/model_test.go` (create if missing)
- Modify: `internal/ui/styles/` (the file that holds existing statusbar style entries)

- [ ] **Step 1: Inspect statusbar.Model to confirm structure**

Run: `grep -nE 'type Model|func.*Model|View\(\)' internal/ui/statusbar/model.go | head -20`

Identify (a) the Model struct, (b) the existing `dirty()`/`Version()` pattern, (c) where `View()` renders the channel name.

- [ ] **Step 2: Write the failing tests**

In `internal/ui/statusbar/model_test.go` (create if it doesn't already exist with the package declaration `package statusbar`), add:

```go
package statusbar

import (
	"strings"
	"testing"
)

func TestSetSyncing_ShowsIndicatorInView(t *testing.T) {
	m := New() // use whatever constructor exists; bare `Model{}` if none
	m.SetChannel("random")

	if strings.Contains(m.View(120), "○") {
		t.Fatal("indicator should NOT be present before SetSyncing(true)")
	}

	m.SetSyncing(true)

	if !strings.Contains(m.View(120), "○") {
		t.Error("indicator should appear after SetSyncing(true)")
	}

	m.SetSyncing(false)

	if strings.Contains(m.View(120), "○") {
		t.Error("indicator should disappear after SetSyncing(false)")
	}
}

func TestSetSyncing_IdempotentOnNoChange(t *testing.T) {
	m := New()
	m.SetSyncing(true)
	verBefore := m.Version() // or m.IsDirty()-equivalent

	m.SetSyncing(true) // same value

	if m.Version() != verBefore {
		t.Error("Version should NOT bump when SetSyncing called with same value")
	}
}
```

If the existing statusbar uses a different constructor name (e.g. accepts arguments), adjust the `New()` call. If the View signature differs (e.g. takes width and height), adjust accordingly. The contract — glyph present when true, absent when false, idempotent — is what matters.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/ui/statusbar/ -count=1`
Expected: FAIL (SetSyncing doesn't exist).

- [ ] **Step 4: Add the field, setter, and View conditional**

In `internal/ui/statusbar/model.go`, add `syncing bool` to the Model struct, the setter:

```go
// SetSyncing toggles a small "verifying" indicator (a single ○ glyph)
// next to the channel name. Used by App's three-tier ChannelSelectedMsg
// dispatch to signal that the displayed cache is being verified
// against the network in the background.
func (m *Model) SetSyncing(syncing bool) {
	if m.syncing == syncing {
		return
	}
	m.syncing = syncing
	m.dirty()
}
```

In the existing `View()` implementation, locate the line that emits the channel name. Adjacent to it, append (or string-concatenate) the styled indicator when `m.syncing` is true:

```go
// Existing rendering of channel name, e.g.:
//   channelLabel := styles.StatusbarChannel.Render("#" + m.channelName)
// becomes:
channelLabel := styles.StatusbarChannel.Render("#" + m.channelName)
if m.syncing {
	channelLabel += " " + styles.StatusbarSyncing.Render("○")
}
```

- [ ] **Step 5: Add the themable style entry**

Find the file that defines `StatusbarChannel` (likely `internal/ui/styles/statusbar.go` or similar). Add next to it:

```go
// StatusbarSyncing styles the small "verifying" glyph that appears
// adjacent to the channel name while a background cache-verify fetch
// is in flight. Uses a muted accent to keep it ignorable.
var StatusbarSyncing = lipgloss.NewStyle().Foreground(Muted)
```

Where `Muted` is whatever existing semantic accent variable the codebase uses for de-emphasized text (consult the same file's existing style entries). If no `Muted` exists, use `lipgloss.AdaptiveColor{Light: "#888", Dark: "#888"}` as a deliberate placeholder and note the theme-pass follow-up in the commit message.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/ui/statusbar/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/statusbar/model.go internal/ui/statusbar/model_test.go internal/ui/styles/
git commit -m "statusbar: SetSyncing toggle and indicator glyph"
```

---

## Phase 2 — App-side wiring

These tasks add the new `App` callbacks, messages, and handler logic. The new code paths are wired but inert until Phase 3 connects them to the cache and Slack client.

### Task 6: Add `WorkspaceReadyMsg.InitialActive` and gate the auto-claim

**Files:**
- Modify: `internal/ui/app.go` (WorkspaceReadyMsg struct + handler)
- Modify: `internal/ui/app_test.go`
- Modify: `cmd/slk/main.go` (callers that construct `WorkspaceReadyMsg` literal)

- [ ] **Step 1: Write the failing test**

Append to `internal/ui/app_test.go`:

```go
func TestWorkspaceReady_OnlyInitialActiveClaimsChannel(t *testing.T) {
	app := NewApp()

	// Two ready messages arriving in the same Update tick; only the
	// InitialActive=true one should set activeTeamID and queue a
	// ChannelSelectedMsg-bearing cmd.
	app.Update(WorkspaceReadyMsg{
		TeamID:   "T-other",
		TeamName: "Other",
		Channels: []sidebar.ChannelItem{{ID: "C-other", Name: "general", Type: "channel"}},
		InitialActive: false,
	})

	if app.activeTeamID != "" {
		t.Errorf("non-initial WorkspaceReady should not set activeTeamID; got %q", app.activeTeamID)
	}

	app.Update(WorkspaceReadyMsg{
		TeamID:        "T-default",
		TeamName:      "Default",
		Channels:      []sidebar.ChannelItem{{ID: "C-default", Name: "general", Type: "channel"}},
		InitialActive: true,
	})

	if app.activeTeamID != "T-default" {
		t.Errorf("activeTeamID = %q, want T-default", app.activeTeamID)
	}
}

func TestWorkspaceReady_BootstrapClaimIsOneShot(t *testing.T) {
	app := NewApp()

	app.Update(WorkspaceReadyMsg{
		TeamID:        "T1",
		TeamName:      "First",
		Channels:      []sidebar.ChannelItem{{ID: "C1", Name: "general", Type: "channel"}},
		InitialActive: true,
	})
	first := app.activeTeamID

	// A second InitialActive=true (defensive — shouldn't happen) is a no-op.
	app.Update(WorkspaceReadyMsg{
		TeamID:        "T2",
		TeamName:      "Second",
		Channels:      []sidebar.ChannelItem{{ID: "C2", Name: "general", Type: "channel"}},
		InitialActive: true,
	})

	if app.activeTeamID != first {
		t.Errorf("activeTeamID changed after second InitialActive; got %q, want %q", app.activeTeamID, first)
	}
}
```

If `sidebar.ChannelItem` isn't already imported in this test file, add the import.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestWorkspaceReady -count=1`
Expected: FAIL — both tests will fail because the field doesn't exist and today's handler ignores team identity.

- [ ] **Step 3: Add the field to WorkspaceReadyMsg**

In `internal/ui/app.go`, find the `WorkspaceReadyMsg struct` declaration (around line 235). Add the field, with the doc comment that anchors the design decision:

```go
WorkspaceReadyMsg struct {
	// ... existing fields ...

	// InitialActive is true for exactly one WorkspaceReadyMsg per
	// program run: the workspace whose team ID matches the configured
	// default_workspace, or — if no default is configured — the first
	// workspace to successfully connect. main.go enforces the
	// uniqueness via sync.Once + atomic router. App's handler treats
	// InitialActive=false as "workspace is up; threads-list kick only".
	InitialActive bool
}
```

- [ ] **Step 4: Add `bootstrapActiveClaimed` field to App**

Find the `App` struct (`type App struct` at the top of the file, search for `activeChannelID string`). Add:

```go
// bootstrapActiveClaimed flips on the first WorkspaceReadyMsg whose
// InitialActive=true is observed. Subsequent InitialActive=true messages
// (defensive — main.go's sync.Once should prevent them) are ignored.
bootstrapActiveClaimed bool
```

- [ ] **Step 5: Rewrite the handler guard**

Replace the existing `if a.activeChannelID == "" {` block in `WorkspaceReadyMsg` (`internal/ui/app.go:2104`) with:

```go
case WorkspaceReadyMsg:
	a.MarkWorkspaceReady(msg.TeamName)
	if msg.InitialActive && !a.bootstrapActiveClaimed {
		a.bootstrapActiveClaimed = true
		a.view = ViewChannels
		a.sidebar.SetThreadsActive(false)
		a.threadsView.SetSummaries(nil)
		a.sidebar.SetThreadsUnreadCount(0)
		a.lastOpenedChannelID = ""
		a.lastOpenedThreadTS = ""
		if msg.Theme != "" {
			styles.Apply(msg.Theme, a.themeOverrides)
			a.messagepane.InvalidateCache()
			a.threadPanel.InvalidateCache()
			a.sidebar.InvalidateCache()
			a.compose.RefreshStyles()
			a.threadCompose.RefreshStyles()
		}
		a.sidebar.SetSectionsProvider(msg.SectionsProvider)
		a.SetChannels(msg.Channels)
		a.channelFinder.SetItems(msg.FinderItems)
		a.SetUserNames(msg.UserNames)
		a.SetCustomEmoji(msg.CustomEmoji)
		a.currentUserID = msg.UserID
		a.activeTeamID = msg.TeamID
		if st, ok := a.statusByTeam[a.activeTeamID]; ok {
			a.statusbar.SetStatus(st.Presence, st.DNDEnabled, st.DNDEndTS)
		} else {
			a.statusbar.SetStatus("", false, time.Time{})
		}
		a.workspaceRail.SelectByID(msg.TeamID)
		if len(msg.Channels) > 0 {
			first := msg.Channels[0]
			a.messagepane.SetLoading(true)
			a.messagepane.SetMessages(nil)
			cmds = append(cmds, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
				return SpinnerTickMsg{}
			}))
			cmds = append(cmds, func() tea.Msg {
				return ChannelSelectedMsg{ID: first.ID, Name: first.Name, Type: first.Type}
			})
		}
	}
	// threadsListFetcher kick stays per-workspace (outside the if).
	if a.threadsListFetcher != nil {
		fetcher := a.threadsListFetcher
		team := msg.TeamID
		cmds = append(cmds, func() tea.Msg { return fetcher(team) })
	}
```

- [ ] **Step 6: Update existing WorkspaceReadyMsg construction sites in main.go**

Run: `grep -n 'WorkspaceReadyMsg{' cmd/slk/main.go`

There should be one call site. Add `InitialActive: false` to the literal for now (Task 18 will replace it with the actual claim logic). This preserves today's behavior temporarily — no workspace claims active — which would break startup if we didn't add a temporary workaround. To keep the build/tests green, set `InitialActive: true` for the first one encountered until Task 18 runs:

Actually, the simplest interim: change the literal to `InitialActive: activeTeamID == wctx.TeamID` so that today's `activeTeamID == wctx.TeamID` post-claimActive logic propagates into the new field. This preserves today's behavior verbatim. Verify that `activeTeamID` is in scope where `WorkspaceReadyMsg` is constructed (look for the surrounding goroutine in `runApp`).

Concrete edit: in `cmd/slk/main.go` where the literal is built, set:

```go
InitialActive: claimActive,
```

(where `claimActive` is the existing local variable defined a few lines earlier — see `cmd/slk/main.go:980-989`).

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run TestWorkspaceReady -count=1`
Expected: PASS.

Also run the full app test suite to confirm nothing else broke:

Run: `go test ./internal/ui/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/app.go internal/ui/app_test.go cmd/slk/main.go
git commit -m "app: WorkspaceReadyMsg.InitialActive gates bootstrap auto-claim

Fixes the bootstrap race where two simultaneous WorkspaceReadyMsgs
both passed the (activeChannelID == \"\") guard and both auto-selected
their first channel."
```

---

### Task 7: Add `SetChannelSyncedAtReader` callback to App

**Files:**
- Modify: `internal/ui/app.go`

- [ ] **Step 1: Find existing reader/fetcher setter style**

Run: `grep -n 'SetChannelCacheReader\|channelCacheReader' internal/ui/app.go | head -10`

Identify the pattern — typically a public setter method and a private field. Mimic that style.

- [ ] **Step 2: Add the field and setter**

Near where `channelCacheReader` is declared in the `App` struct, add:

```go
// channelSyncedAtReader returns the unix timestamp (seconds) at which
// the channel's cache was last authoritatively replaced, or 0 if
// never. Used by ChannelSelectedMsg's three-tier dispatch to decide
// between cache-only, cache-and-verify, and spinner-only render.
// Nil reader defaults to 0 (spinner-only tier always).
channelSyncedAtReader func(channelID string) int64
```

Near `SetChannelCacheReader` (search for it in the file), add the setter:

```go
// SetChannelSyncedAtReader installs the cache-freshness reader. Wired
// in cmd/slk/main.go's wireCallbacks to db.GetChannelSyncedAt.
func (a *App) SetChannelSyncedAtReader(fn func(channelID string) int64) {
	a.channelSyncedAtReader = fn
}
```

- [ ] **Step 3: Run tests to verify nothing broke**

Run: `go test ./internal/ui/ -count=1`
Expected: PASS (no behavior change yet; field is added but unused).

- [ ] **Step 4: Commit**

```bash
git add internal/ui/app.go
git commit -m "app: SetChannelSyncedAtReader callback (no behavior wired yet)"
```

---

### Task 8: Add `SetChannelReadMarker` callback to App

**Files:**
- Modify: `internal/ui/app.go`

- [ ] **Step 1: Add field and setter**

Near `channelFetcher` in the `App` struct, add:

```go
// channelReadMarker fires a Slack MarkChannel + cache.UpdateLastReadTS
// for the given channel up to ts. Returns a tea.Msg (typically
// ChannelMarkedReadMsg). Wired in cmd/slk/main.go's wireCallbacks.
// Tier 1 of ChannelSelectedMsg (cache fresh, no GetHistory) uses this
// to keep mark-as-read working without firing the full fetcher.
channelReadMarker func(channelID, ts string) tea.Msg
```

Near `SetChannelFetcher`, add the setter:

```go
// SetChannelReadMarker installs the mark-as-read callback. Wired in
// cmd/slk/main.go alongside SetChannelFetcher (the fetcher keeps its
// own mark-as-read side-effect for Tier 2/3; this callback exists
// purely so Tier 1 can mark-as-read without GetHistory).
func (a *App) SetChannelReadMarker(fn func(channelID, ts string) tea.Msg) {
	a.channelReadMarker = fn
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/ui/ -count=1`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/ui/app.go
git commit -m "app: SetChannelReadMarker callback (no behavior wired yet)"
```

---

### Task 9: Add `UserResolvedMsg` type and handler

**Files:**
- Modify: `internal/ui/app.go`
- Modify: `internal/ui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/app_test.go`:

```go
func TestUserResolvedMsg_PatchesActiveWorkspace(t *testing.T) {
	app := NewApp()
	app.activeTeamID = "T1"
	// Seed a message authored by U1 with the placeholder name.
	app.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1.0", UserID: "U1", UserName: "U1", Text: "hi"},
	})

	app.Update(UserResolvedMsg{
		TeamID:      "T1",
		UserID:      "U1",
		DisplayName: "alice",
	})

	got := app.messagepane.Messages()
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].UserName != "alice" {
		t.Errorf("UserName = %q, want alice", got[0].UserName)
	}
}

func TestUserResolvedMsg_DropsForOtherWorkspace(t *testing.T) {
	app := NewApp()
	app.activeTeamID = "T1"
	app.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1.0", UserID: "U1", UserName: "U1", Text: "hi"},
	})

	app.Update(UserResolvedMsg{
		TeamID:      "T-other",
		UserID:      "U1",
		DisplayName: "alice",
	})

	got := app.messagepane.Messages()
	if got[0].UserName != "U1" {
		t.Errorf("UserName changed despite wrong team; got %q", got[0].UserName)
	}
}
```

If `messages.Model.Messages()` doesn't exist as an accessor, either (a) check the existing test file for the canonical accessor used elsewhere and use that, or (b) add a `Messages() []MessageItem` getter to `messages.Model` (test helper) and document it as such.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run TestUserResolvedMsg -count=1`
Expected: FAIL (type / handler doesn't exist).

- [ ] **Step 3: Add the message type**

In `internal/ui/app.go`, near the other Msg type declarations (search for `DMNameResolvedMsg struct`), add:

```go
// UserResolvedMsg arrives asynchronously after main.go's per-workspace
// userResolver completes a users.info round-trip for a previously-
// unknown message author. The handler patches the in-memory display
// name on the messagepane and threadPanel so rows authored by this
// user re-render with the real name on the next View().
UserResolvedMsg struct {
	TeamID      string
	UserID      string
	DisplayName string
	IsBot       bool
}
```

- [ ] **Step 4: Add the case handler**

In the main `Update` switch in `internal/ui/app.go`, alongside `case DMNameResolvedMsg:` (search for it), add:

```go
case UserResolvedMsg:
	if msg.TeamID != a.activeTeamID {
		break
	}
	a.messagepane.PatchUserName(msg.UserID, msg.DisplayName)
	a.threadPanel.PatchUserName(msg.UserID, msg.DisplayName)
	// IsBot affects DM channel-type classification, but that's
	// orchestrated by DMNameResolvedMsg; this handler is only the
	// in-history name patch. IsBot is carried for forward
	// compatibility but not consumed here.
```

If a `Messages()` accessor is needed for the tests, add to `internal/ui/messages/model.go`:

```go
// Messages returns the current message slice. Test-only accessor;
// production code should mutate via SetMessages / AppendMessage /
// PatchUserName.
func (m *Model) Messages() []MessageItem { return m.messages }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run TestUserResolvedMsg -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/app.go internal/ui/app_test.go internal/ui/messages/model.go
git commit -m "app: UserResolvedMsg type and handler

Patches in-history UserName for the active workspace; drops messages
from other workspaces (a fetch initiated under workspace A but
arriving after the user switches to B has nothing useful to do)."
```

---

### Task 10: Three-tier `ChannelSelectedMsg` dispatch

**Files:**
- Modify: `internal/ui/app.go` (ChannelSelectedMsg handler)
- Modify: `internal/ui/app_test.go`

- [ ] **Step 1: Add the tier constants**

In `internal/ui/app.go`, near the top of the file (after the import block, with the other top-level constants), add:

```go
const (
	// cacheFreshThreshold: cache rendered as-is, no network fetch, no
	// syncing indicator. Channel was visited or WS-updated within this
	// window so the SQLite snapshot is provably recent.
	cacheFreshThreshold = 30 * time.Second

	// cacheStaleThreshold: above this age, cache-first render is
	// suppressed entirely and a spinner is shown until the network
	// fetch returns. The 30-second...5-minute middle band gets the
	// cache-first + verify-in-background treatment with the syncing
	// indicator.
	cacheStaleThreshold = 5 * time.Minute
)
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/ui/app_test.go`. The tests use a small recursive drain helper to walk `tea.BatchMsg` returned by `Update`, mirroring the existing `drainForPermalinkCopied` pattern at `internal/ui/app_test.go:438-456`:

```go
// drainAllCmds recursively executes every cmd inside the tea.BatchMsg
// tree returned by Update. Used to surface counter side-effects on
// closure-bound fakes (channelFetcher, channelReadMarker). The
// resulting tea.Msgs are NOT fed back into Update — these tests only
// care about whether the fakes were invoked.
func drainAllCmds(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drainAllCmds(t, c)
		}
	}
}

func TestChannelSelected_Tier1_RenderCacheNoFetch(t *testing.T) {
	app := NewApp()
	now := time.Now().Unix()
	app.SetChannelSyncedAtReader(func(id string) int64 { return now - 10 }) // fresh
	app.SetChannelCacheReader(func(id string) []messages.MessageItem {
		return []messages.MessageItem{{TS: "1.0", UserID: "U", UserName: "u", Text: "hi"}}
	})
	fetchCalled := 0
	app.SetChannelFetcher(func(id, name string) tea.Msg {
		fetchCalled++
		return MessagesLoadedMsg{ChannelID: id, Messages: nil}
	})
	markCalled := 0
	app.SetChannelReadMarker(func(id, ts string) tea.Msg {
		markCalled++
		return nil
	})

	_, cmd := app.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	drainAllCmds(t, cmd)

	if fetchCalled != 0 {
		t.Errorf("Tier 1: fetcher should NOT fire; got %d calls", fetchCalled)
	}
	if markCalled != 1 {
		t.Errorf("Tier 1: markRead should fire once; got %d calls", markCalled)
	}
}

func TestChannelSelected_Tier2_CacheAndFetch(t *testing.T) {
	app := NewApp()
	now := time.Now().Unix()
	app.SetChannelSyncedAtReader(func(id string) int64 { return now - 120 }) // 2 min — stale
	app.SetChannelCacheReader(func(id string) []messages.MessageItem {
		return []messages.MessageItem{{TS: "1.0", UserID: "U", UserName: "u", Text: "hi"}}
	})
	fetchCalled := 0
	app.SetChannelFetcher(func(id, name string) tea.Msg {
		fetchCalled++
		return MessagesLoadedMsg{ChannelID: id, Messages: nil}
	})
	markCalled := 0
	app.SetChannelReadMarker(func(id, ts string) tea.Msg {
		markCalled++
		return nil
	})

	_, cmd := app.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	drainAllCmds(t, cmd)

	if fetchCalled != 1 {
		t.Errorf("Tier 2: fetcher should fire once; got %d", fetchCalled)
	}
	if markCalled != 0 {
		t.Errorf("Tier 2: markRead should NOT fire (fetcher's own mark-as-read handles it); got %d", markCalled)
	}
}

func TestChannelSelected_Tier3_SpinnerOnly(t *testing.T) {
	app := NewApp()
	app.SetChannelSyncedAtReader(func(id string) int64 { return 0 }) // never synced
	app.SetChannelCacheReader(func(id string) []messages.MessageItem {
		return []messages.MessageItem{{TS: "1.0", UserID: "U", UserName: "u", Text: "hi"}}
	})
	fetchCalled := 0
	app.SetChannelFetcher(func(id, name string) tea.Msg {
		fetchCalled++
		return MessagesLoadedMsg{ChannelID: id, Messages: nil}
	})

	_, cmd := app.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	drainAllCmds(t, cmd)

	if got := app.messagepane.Messages(); len(got) != 0 {
		t.Errorf("Tier 3: pane should be empty (spinner); got %d msgs", len(got))
	}
	if fetchCalled != 1 {
		t.Errorf("Tier 3: fetcher should fire once; got %d", fetchCalled)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run TestChannelSelected_Tier -count=1`
Expected: FAIL.

- [ ] **Step 4: Rewrite the cache-first block in ChannelSelectedMsg handler**

In `internal/ui/app.go`, locate the existing block at lines 1401-1431 (cache reader + fetcher dispatch). Replace it with:

```go
var cached []messages.MessageItem
if a.channelCacheReader != nil {
	cached = a.channelCacheReader(msg.ID)
}
var syncedAt int64
if a.channelSyncedAtReader != nil {
	syncedAt = a.channelSyncedAtReader(msg.ID)
}
age := time.Duration(0)
if syncedAt > 0 {
	age = time.Since(time.Unix(syncedAt, 0))
}
debuglog.Cache("ChannelSelectedMsg: channel=%s name=%q cache_hit_count=%d synced_at=%d age_ms=%d",
	msg.ID, msg.Name, len(cached), syncedAt, age.Milliseconds())

fireFetch := func() {
	if a.channelFetcher == nil {
		debuglog.Cache("ChannelSelectedMsg: channel=%s no channelFetcher wired", msg.ID)
		return
	}
	fetcher := a.channelFetcher
	chID, chName := msg.ID, msg.Name
	debuglog.Cache("ChannelSelectedMsg: channel=%s firing background network fetch", msg.ID)
	cmds = append(cmds, func() tea.Msg { return fetcher(chID, chName) })
}

switch {
case syncedAt > 0 && age < cacheFreshThreshold && len(cached) > 0:
	// Tier 1: cache fresh; render and mark-as-read, no fetch.
	a.messagepane.SetLoading(false)
	a.messagepane.SetMessages(cached)
	a.statusbar.SetSyncing(false)
	if a.channelReadMarker != nil && len(cached) > 0 {
		marker := a.channelReadMarker
		chID := msg.ID
		latestTS := cached[len(cached)-1].TS
		cmds = append(cmds, func() tea.Msg { return marker(chID, latestTS) })
	}
	debuglog.Cache("ChannelSelectedMsg: channel=%s tier=1_fresh", msg.ID)

case syncedAt > 0 && age < cacheStaleThreshold && len(cached) > 0:
	// Tier 2: cache-first + verify.
	a.messagepane.SetLoading(false)
	a.messagepane.SetMessages(cached)
	a.statusbar.SetSyncing(true)
	fireFetch()
	debuglog.Cache("ChannelSelectedMsg: channel=%s tier=2_verify", msg.ID)

default:
	// Tier 3: stale or never-synced — spinner only.
	a.messagepane.SetLoading(true)
	a.messagepane.SetMessages(nil)
	a.statusbar.SetSyncing(false)
	cmds = append(cmds, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return SpinnerTickMsg{}
	}))
	fireFetch()
	debuglog.Cache("ChannelSelectedMsg: channel=%s tier=3_spinner", msg.ID)
}
```

- [ ] **Step 5: Update `MessagesLoadedMsg` handler to clear SetSyncing**

In the `case MessagesLoadedMsg:` block (`internal/ui/app.go:1433`), at the top of the `if msg.ChannelID == a.activeChannelID {` body, add:

```go
a.statusbar.SetSyncing(false)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run TestChannelSelected_Tier -count=1`
Expected: PASS.

Also run the full ui suite to confirm no regressions:

Run: `go test ./internal/ui/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/app.go internal/ui/app_test.go
git commit -m "app: three-tier ChannelSelectedMsg dispatch + MessagesLoadedMsg clears syncing"
```

---

### Task 11: Remove `WorkspaceSwitchedMsg` pane wipe

**Files:**
- Modify: `internal/ui/app.go` (WorkspaceSwitchedMsg handler)
- Modify: `internal/ui/app_test.go` (any tests asserting on the intermediate state)

- [ ] **Step 1: Locate the wipe block**

In `internal/ui/app.go:1999-2004`, find:

```go
a.compose.Reset()
a.messagepane.SetLoading(true)
a.messagepane.SetMessages(nil)
cmds = append(cmds, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
	return SpinnerTickMsg{}
}))
```

- [ ] **Step 2: Replace the wipe**

Replace those three lines (the `SetLoading`, `SetMessages(nil)`, and the spinner-tick `cmds = append`) with a clear-on-empty-workspace guard. The full replacement keeps `compose.Reset()`:

```go
a.compose.Reset()
a.statusbar.SetSyncing(false) // defensive: don't carry stale sync state across workspaces
// Pane is left as-is — the queued ChannelSelectedMsg below will paint
// over it (Tier 1/2/3). For empty workspaces (no Channels) the pane
// is cleared explicitly below.
```

Then locate the empty-workspace branch lower in the handler (`if len(msg.Channels) == 0 {`). Today's code calls `a.sidebar.SelectThreadsRow()`. Add the explicit pane clear there:

```go
} else {
	a.sidebar.SelectThreadsRow()
	a.messagepane.SetLoading(false)
	a.messagepane.SetMessages(nil)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/ui/ -count=1`
Expected: PASS, with potentially a small number of failures in tests that asserted on the intermediate `SetMessages(nil)` state. If any fail, update them to assert on the final state (post-`ChannelSelectedMsg`) instead.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/app.go internal/ui/app_test.go
git commit -m "app: drop WorkspaceSwitchedMsg pane wipe

The wipe + spinner caused [N old msgs] -> [empty + spinner] -> [N cached
msgs] -> [N fresh msgs] in under 100ms. The queued ChannelSelectedMsg
that follows handles the repaint cleanly via three-tier dispatch."
```

---

## Phase 3 — main.go workspace plumbing

These tasks introduce the active-pointer router, refactor `wireCallbacks` to read it at invocation time, and wire the new App callbacks introduced in Phase 2.

### Task 12: Create `workspaceRouter` type

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Add the type**

In `cmd/slk/main.go`, near the `WorkspaceContext` struct declaration (search for `type WorkspaceContext struct`), add:

```go
// workspaceRouter holds the program-wide "active workspace" pointer.
// wireCallbacks(router) is invoked ONCE at startup. Every workspace-
// scoped callback reads router.Active() at invocation time so the
// effective workspace tracks the user's current Ctrl-N selection
// without any closure rebinding.
//
// The `all` map is populated only during the connect-workspaces phase
// (before p.Run); subsequent reads from p.Send-invoked callbacks are
// race-free without a mutex.
type workspaceRouter struct {
	active atomic.Pointer[WorkspaceContext]
	all    map[string]*WorkspaceContext
}

func newWorkspaceRouter() *workspaceRouter {
	return &workspaceRouter{all: map[string]*WorkspaceContext{}}
}

func (r *workspaceRouter) Active() *WorkspaceContext { return r.active.Load() }
func (r *workspaceRouter) Set(wctx *WorkspaceContext) { r.active.Store(wctx) }
func (r *workspaceRouter) ByID(teamID string) *WorkspaceContext {
	return r.all[teamID]
}
```

Add the `sync/atomic` import at the top of the file if it isn't already there.

- [ ] **Step 2: Verify build**

Run: `go build ./cmd/slk`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: workspaceRouter holds active workspace pointer"
```

---

### Task 13: Refactor `wireCallbacks` to take router; called once at startup

**Files:**
- Modify: `cmd/slk/main.go`

This is the load-bearing refactor. We change the signature and body of `wireCallbacks` and the surrounding control flow but preserve every existing callback's externally-visible behavior.

- [ ] **Step 1: Change `wireCallbacks` signature**

Find `wireCallbacks := func(wctx *WorkspaceContext) {` (around `cmd/slk/main.go:617`). Change it to:

```go
wireCallbacks := func(router *workspaceRouter) {
```

- [ ] **Step 2: Rewrite each callback body to read `router.Active()`**

Inside `wireCallbacks`, every callback body that previously used `client`, `userNames`, `lastReadMap`, etc. (the captured-from-`wctx` locals) now reads them from `router.Active()` at invocation time. Pattern:

Before:
```go
app.SetChannelFetcher(func(channelID, channelName string) tea.Msg {
	msgItems := fetchChannelMessages(client, channelID, db, userNames, tsFormat, avatarCache)
	lastReadTS := lastReadMap[channelID]
	...
})
```

After:
```go
app.SetChannelFetcher(func(channelID, channelName string) tea.Msg {
	wctx := router.Active()
	if wctx == nil {
		return nil
	}
	msgItems := fetchChannelMessages(wctx.Client, channelID, db, wctx.UserNames, tsFormat, avatarCache)
	lastReadTS := wctx.LastReadMap[channelID]
	...
})
```

Apply this transformation to every `app.Set*` call inside `wireCallbacks`:

- `SetChannelLastReadFetcher`
- `SetChannelVisitRecorder`
- `SetChannelLookupFunc`
- `SetChannelCacheReader`
- `SetChannelFetcher`
- `SetMessageSender`
- `SetMessageEditor`
- (etc. — work through them in source order)

For each callback, replace the captured `client`, `userNames`, `lastReadMap`, `wctx`, etc. with their `router.Active()` equivalents. Note: in callbacks where the captured variable was `wctx` itself (e.g. `SetChannelVisitRecorder` reads `wctx.LastVisitedByChannel`), the rewrite is `wctx := router.Active(); ... wctx.LastVisitedByChannel ...`.

Once all callbacks have been rewritten, delete the now-unused local-variable bindings at the top of `wireCallbacks`:

```go
// REMOVE these lines from the top of wireCallbacks:
client := wctx.Client
userNames := wctx.UserNames
lastReadMap := wctx.LastReadMap
```

- [ ] **Step 3: Update the two existing call sites**

Find the two calls to `wireCallbacks(wctx)` in `cmd/slk/main.go`:

- Inside `app.SetWorkspaceSwitcher` (around line 916): **delete this call** (the `router.Set(wctx)` call in Task 14 replaces it).
- Inside the per-workspace connect goroutine (around line 988): **delete this call**.

Then add a single call to `wireCallbacks(router)` after the router is constructed but before `p.Run()`. The router must be constructed *before* `wireCallbacks` is invoked. The cleanest place is right after `app := ui.NewApp()` and just after the router declaration:

```go
router := newWorkspaceRouter()

// (existing code: build callbacks, etc.)

wireCallbacks(router)

// ... eventually:
_, err = p.Run()
```

- [ ] **Step 4: Update `app.SetWorkspaceSwitcher`**

Find `app.SetWorkspaceSwitcher(...)` (around line 906). Replace its body with:

```go
app.SetWorkspaceSwitcher(func(teamID string) tea.Msg {
	wctx := router.ByID(teamID)
	if wctx == nil {
		return nil
	}
	router.Set(wctx)
	activeTeamID = teamID  // keep the existing local for any non-router consumers
	return ui.WorkspaceSwitchedMsg{
		TeamID:           wctx.TeamID,
		TeamName:         wctx.TeamName,
		Theme:            cfg.ResolveTheme(teamID),
		Channels:         wctx.Channels,
		FinderItems:      wctx.FinderItems,
		UserNames:        wctx.UserNames,
		UserID:           wctx.UserID,
		CustomEmoji:      wctx.CustomEmoji,
		SectionsProvider: sectionsProviderAdapter{store: wctx.SectionStore},
	}
})
```

- [ ] **Step 5: Populate `router.all` in the connect goroutine**

In the connect goroutine where `workspaces[wctx.TeamID] = wctx` is set (around line 973), also add:

```go
router.all[wctx.TeamID] = wctx
```

(`router.all` is map access during the connect phase, which is sequential per workspace but parallel across workspaces. SQLite-style serialization isn't guaranteed here — wrap with a mutex if `go test -race` complains; otherwise leave as-is since each goroutine writes a distinct key.)

If `-race` complains, add:

```go
var routerMu sync.Mutex
// ... in connect goroutine:
routerMu.Lock()
router.all[wctx.TeamID] = wctx
routerMu.Unlock()
```

- [ ] **Step 6: Verify build, run all tests with race detector**

Run: `make build && make test`
Expected: build succeeds; all tests pass; no race detector warnings.

- [ ] **Step 7: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: wireCallbacks reads router.Active() at invocation time

Callbacks bound once at startup. Workspace switch becomes
router.Set(wctx). No more per-switch closure rebuilding."
```

---

### Task 14: `firstReady sync.Once` sets `WorkspaceReadyMsg.InitialActive`

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Add package-level state**

Near the top of `runApp` (or as package-level if structurally cleaner), alongside `defaultTeamID`:

```go
var firstReady sync.Once
```

- [ ] **Step 2: Compute `isInitial` per-workspace**

In the connect goroutine, replace the existing `claimActive` block (around `cmd/slk/main.go:980-989`):

```go
claimActive := false
if defaultTeamID != "" {
	claimActive = wctx.TeamID == defaultTeamID
} else {
	claimActive = activeTeamID == ""
}
if claimActive {
	activeTeamID = wctx.TeamID
	wireCallbacks(wctx)  // (already removed in Task 13)
}
```

…with:

```go
isInitial := false
if defaultTeamID != "" {
	if wctx.TeamID == defaultTeamID {
		isInitial = true
		router.Set(wctx)
		activeTeamID = wctx.TeamID
	}
	// else: not the configured default; never claim.
} else {
	firstReady.Do(func() {
		isInitial = true
		router.Set(wctx)
		activeTeamID = wctx.TeamID
	})
}
```

- [ ] **Step 3: Update the `WorkspaceReadyMsg` construction**

Change:

```go
InitialActive: claimActive,
```

to:

```go
InitialActive: isInitial,
```

- [ ] **Step 4: Verify build and run tests**

Run: `make build && make test`
Expected: success.

- [ ] **Step 5: Manual sanity check (optional)**

```bash
SLK_DEBUG=1 ./bin/slk
# Open log
grep -E 'WorkspaceReady|wireCallbacks|activeTeamID' slk-debug.log
```

Expected: only one workspace's `WorkspaceReadyMsg` triggers the auto-select branch; no `channel_not_found` errors on either workspace's first channel.

- [ ] **Step 6: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: firstReady sync.Once + isInitial drives WorkspaceReadyMsg.InitialActive

Resolves the bootstrap race: exactly one workspace claims initial-active,
matching the router state set in the same goroutine."
```

---

### Task 15: Wire `channelSyncedAtReader` callback in main.go

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Add the setter call inside `wireCallbacks`**

Inside `wireCallbacks(router)`, after `app.SetChannelCacheReader(...)`, add:

```go
app.SetChannelSyncedAtReader(func(channelID string) int64 {
	return db.GetChannelSyncedAt(channelID)
})
```

- [ ] **Step 2: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: wire SetChannelSyncedAtReader to db.GetChannelSyncedAt"
```

---

### Task 16: Split mark-as-read into `channelReadMarker` callback

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Locate the mark-as-read side-effect inside `channelFetcher`**

In `cmd/slk/main.go`, inside the `app.SetChannelFetcher(...)` closure (around lines 663-673), the existing block:

```go
if len(msgItems) > 0 {
	latestTS := msgItems[len(msgItems)-1].TS
	go func() {
		_ = client.MarkChannel(ctx, channelID, latestTS)
		_ = db.UpdateLastReadTS(channelID, latestTS)
		lastReadMap[channelID] = latestTS
		if p != nil {
			p.Send(ui.ChannelMarkedReadMsg{ChannelID: channelID})
		}
	}()
}
```

- [ ] **Step 2: Extract a helper**

In `cmd/slk/main.go`, add a free function (before `runApp`):

```go
// markChannelReadAsync fires Slack's conversations.mark plus the local
// LastReadTS persistence in a background goroutine. Returns
// immediately. Caller wires whatever tea.Msg they want emitted on
// completion (typically ChannelMarkedReadMsg); pass nil to suppress.
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
	lastReadMap := wctx.LastReadMap
	go func() {
		_ = client.MarkChannel(ctx, channelID, ts)
		_ = db.UpdateLastReadTS(channelID, ts)
		lastReadMap[channelID] = ts
		if p != nil {
			p.Send(ui.ChannelMarkedReadMsg{ChannelID: channelID})
		}
	}()
}
```

- [ ] **Step 3: Use the helper inside `channelFetcher`**

Replace the inline mark-as-read block with:

```go
if len(msgItems) > 0 {
	wctx := router.Active()
	latestTS := msgItems[len(msgItems)-1].TS
	markChannelReadAsync(ctx, wctx, db, p, channelID, latestTS)
}
```

- [ ] **Step 4: Wire `SetChannelReadMarker` to the same helper**

Inside `wireCallbacks(router)`, after `app.SetChannelFetcher(...)`, add:

```go
app.SetChannelReadMarker(func(channelID, ts string) tea.Msg {
	wctx := router.Active()
	markChannelReadAsync(ctx, wctx, db, p, channelID, ts)
	return nil // ChannelMarkedReadMsg is emitted from inside the goroutine
})
```

- [ ] **Step 5: Run tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: split mark-as-read into channelReadMarker callback

Tier 1 of ChannelSelectedMsg (cache fresh, no GetHistory) now fires
mark-as-read via this dedicated callback. Tier 2/3 mark-as-read stays
inline in channelFetcher (identical behavior to today)."
```

---

### Task 17: Bump `synced_at` from `fetchChannelMessages` and `OnMessage`

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Bump after authoritative replace in `fetchChannelMessages`**

In `cmd/slk/main.go:fetchChannelMessages`, immediately before the final `return msgItems` (around line 1879), add:

```go
if err := db.SetChannelSyncedAt(channelID, time.Now().Unix()); err != nil {
	debuglog.Cache("fetchChannelMessages: SetChannelSyncedAt %s: %v", channelID, err)
}
```

- [ ] **Step 2: Bump in `rtmEventHandler.OnMessage`**

In `cmd/slk/main.go:OnMessage`, immediately after the `h.db.UpsertMessage(...)` block (around lines 2106-2117), add:

```go
if h.db != nil {
	if err := h.db.SetChannelSyncedAt(channelID, time.Now().Unix()); err != nil {
		debuglog.Cache("OnMessage: SetChannelSyncedAt %s: %v", channelID, err)
	}
}
```

- [ ] **Step 3: Verify build and tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: bump channels.synced_at on history fetch and WS-driven upsert

A successful GetHistory or any WS-delivered new message proves the
cache is up-to-date through time.Now(); record it so subsequent
channel-selects can take the Tier 1 fast path."
```

---

## Phase 4 — Async user resolution

### Task 18: `userResolver` type + attach to `WorkspaceContext`

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Add the type**

In `cmd/slk/main.go`, after the `workspaceRouter` declaration, add:

```go
// userResolver dispatches users.info lookups for unknown message
// authors in the background. Deduplicates concurrent requests for
// the same userID; failures are silent (the row stays rendered as
// its user ID). Bound to a single workspace because user IDs are
// workspace-scoped.
type userResolver struct {
	teamID    string
	client    *slackclient.Client
	db        *cache.DB
	avatars   *avatar.Cache
	userNames map[string]string
	send      func(tea.Msg)
	inflight  sync.Map // userID -> struct{}
}

func newUserResolver(
	teamID string,
	client *slackclient.Client,
	db *cache.DB,
	avatars *avatar.Cache,
	userNames map[string]string,
	send func(tea.Msg),
) *userResolver {
	return &userResolver{
		teamID:    teamID,
		client:    client,
		db:        db,
		avatars:   avatars,
		userNames: userNames,
		send:      send,
	}
}

// Request enqueues a users.info fetch for userID. Returns immediately.
// On success, emits a ui.UserResolvedMsg via the resolver's send
// callback so the App can patch in-history display names live.
func (r *userResolver) Request(userID string) {
	if r == nil || userID == "" {
		return
	}
	if _, exists := r.inflight.LoadOrStore(userID, struct{}{}); exists {
		return
	}
	go func() {
		defer r.inflight.Delete(userID)
		u, err := r.client.GetUserProfile(userID)
		if err != nil {
			debuglog.Cache("userResolver: GetUserProfile team=%s user=%s err=%v",
				r.teamID, userID, err)
			return
		}
		name := u.Profile.DisplayName
		if name == "" {
			name = u.RealName
		}
		if name == "" {
			name = u.Name
		}
		isBot := u.IsBot || u.IsAppUser
		r.userNames[userID] = name
		r.avatars.Preload(userID, u.Profile.Image32)
		_ = r.db.UpsertUser(cache.User{
			ID:          userID,
			WorkspaceID: r.teamID,
			Name:        u.Name,
			DisplayName: name,
			AvatarURL:   u.Profile.Image32,
			Presence:    "away",
			IsBot:       isBot,
		})
		if r.send != nil {
			r.send(ui.UserResolvedMsg{
				TeamID:      r.teamID,
				UserID:      userID,
				DisplayName: name,
				IsBot:       isBot,
			})
		}
	}()
}
```

- [ ] **Step 2: Add field to `WorkspaceContext`**

Find `type WorkspaceContext struct` and add:

```go
UserResolver *userResolver
```

- [ ] **Step 3: Construct in `connectWorkspace`**

Find `connectWorkspace` (around `cmd/slk/main.go:1088`). After the existing `wctx := &WorkspaceContext{...}` initialization, add:

```go
wctx.UserResolver = newUserResolver(
	wctx.TeamID,
	wctx.Client,
	db,
	avatarCache,
	wctx.UserNames,
	func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	},
)
```

If `p` isn't in scope where `connectWorkspace` is called, defer wiring `send` to after `p` is created. Concretely: leave `send: nil` in the constructor here and patch it after `p = tea.NewProgram(app)` (around `cmd/slk/main.go:954`):

```go
// After p is created:
for _, w := range workspaces {
	if w.UserResolver != nil {
		w.UserResolver.send = func(msg tea.Msg) { p.Send(msg) }
	}
}
```

But cleaner: since `connectWorkspace` runs in a goroutine started AFTER `p.Run()` is called, `p` IS already non-nil. So passing it into the function signature is fine. Update the `connectWorkspace` signature to accept `*tea.Program`:

```go
func connectWorkspace(
	ctx context.Context,
	token slackclient.Token,
	db *cache.DB,
	cfg config.Config,
	avatarCache *avatar.Cache,
	p *tea.Program,  // NEW
) (*WorkspaceContext, error) {
```

Update all call sites accordingly (search `connectWorkspace(` and add `p`).

- [ ] **Step 4: Verify build and tests**

Run: `make build && make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: userResolver per workspace; attached to WorkspaceContext"
```

---

### Task 19: `resolveUserCached` helper + use it in `fetchChannelMessages`

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Add the cached-only helper**

Near `resolveUser` (around `cmd/slk/main.go:1458`), add a new function:

```go
// resolveUserCached returns the display name for userID using only
// local sources: the in-memory userNames map and the cached users
// table. Never hits the network. Returns ("", false) when the user
// is unknown — caller is expected to fall back to userID-as-name and
// enqueue an async lookup via wctx.UserResolver.Request.
func resolveUserCached(userID string, userNames map[string]string, db *cache.DB) (string, bool) {
	if userID == "" {
		return "", false
	}
	if name, ok := userNames[userID]; ok && name != "" {
		return name, true
	}
	if u, err := db.GetUser(userID); err == nil {
		name := u.DisplayName
		if name == "" {
			name = u.Name
		}
		if name != "" {
			userNames[userID] = name
			return name, true
		}
	}
	return "", false
}
```

- [ ] **Step 2: Update `fetchChannelMessages`**

In `fetchChannelMessages`, replace the call to `resolveUser`:

```go
// OLD:
// userName, _ := resolveUser(client, m.User, userNames, db, avatarCache)

// NEW:
userName, ok := resolveUserCached(m.User, userNames, db)
if !ok {
	userName = m.User
	if router != nil {
		if wctx := router.Active(); wctx != nil && wctx.UserResolver != nil {
			wctx.UserResolver.Request(m.User)
		}
	}
}
```

`fetchChannelMessages` does not currently accept `router`. Update its signature:

```go
func fetchChannelMessages(
	client *slackclient.Client,
	channelID string,
	db *cache.DB,
	userNames map[string]string,
	tsFormat string,
	avatarCache *avatar.Cache,
	router *workspaceRouter,  // NEW
) []messages.MessageItem {
```

And update the caller inside `wireCallbacks`:

```go
msgItems := fetchChannelMessages(wctx.Client, channelID, db, wctx.UserNames, tsFormat, avatarCache, router)
```

- [ ] **Step 3: Run tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: fetchChannelMessages uses resolveUserCached + async resolver"
```

---

### Task 20: Async resolution in `fetchOlderMessages`, `fetchThreadReplies`, `enrichCachedRow`, `OnMessage`

**Files:**
- Modify: `cmd/slk/main.go`

- [ ] **Step 1: Update `fetchOlderMessages`**

Apply the same signature update (add `router *workspaceRouter`) and inline pattern from Task 19 to `fetchOlderMessages`. The call site is in `wireCallbacks` — search for `fetchOlderMessages(`.

- [ ] **Step 2: Update `fetchThreadReplies`**

Same pattern. Add `router *workspaceRouter` to its signature, replace inline `resolveUser` calls, update the call site.

- [ ] **Step 3: Update `enrichCachedRow`**

`enrichCachedRow` does not currently call `resolveUser` (it pulls from `userNames` and `db.GetUser` already, falling back to userID). The change here is to additionally call `wctx.UserResolver.Request(m.UserID)` when neither source resolves the name. Since `enrichCachedRow` doesn't currently have access to `router`, add it as a parameter and thread through to both call sites (`loadCachedMessages` and `loadCachedThreadReplies`).

In `enrichCachedRow`, change the fallback from:

```go
if userName == "" {
	userName = m.UserID
}
```

to:

```go
if userName == "" {
	userName = m.UserID
	if router != nil {
		if wctx := router.Active(); wctx != nil && wctx.UserResolver != nil {
			wctx.UserResolver.Request(m.UserID)
		}
	}
}
```

- [ ] **Step 4: Update `OnMessage`**

In `rtmEventHandler.OnMessage`, the `userName := userID; if resolved, ok := h.userNames[userID]; ok { userName = resolved }` block becomes:

```go
userName, ok := resolveUserCached(userID, h.userNames, h.db)
if !ok {
	userName = userID
	if h.wsCtx != nil && h.wsCtx.UserResolver != nil {
		h.wsCtx.UserResolver.Request(userID)
	}
}
```

- [ ] **Step 5: Drop the synchronous `resolveUser` call sites**

Search `cmd/slk/main.go` for remaining `resolveUser(` call sites. The only ones that should remain are:

- Inside `resolveUser` itself (the function definition).
- Possibly in DM-resolution / browseable-channel paths that are explicitly meant to be synchronous.

For each remaining synchronous call site, evaluate: does this NEED to block on a Slack profile fetch? If yes (e.g. DM peer name resolution before first render), keep it. If no, replace with `resolveUserCached + Request` pattern.

`resolveUser` itself can stay in the file for backward compatibility with any remaining synchronous callers, but the in-history hot paths must not use it.

- [ ] **Step 6: Run tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/slk/main.go
git commit -m "main: async user resolution in fetchOlderMessages, threads, OnMessage, cache enrich

History-processing paths no longer block on per-user users.info calls.
Unknown authors render as their user ID; UserResolvedMsg patches the
display name live."
```

---

## Phase 5 — Verification

### Task 21: Full test suite + lint

**Files:**
- None modified.

- [ ] **Step 1: Run full test suite with race detector**

Run: `make test`
Expected: PASS, no race warnings.

If any tests fail that aren't covered by earlier task updates, investigate and fix in-place. Likely candidates:

- Tests that asserted on the intermediate `SetMessages(nil)` state in `WorkspaceSwitchedMsg` (Task 11): update to assert on the final post-`ChannelSelectedMsg` state.
- Tests that constructed `WorkspaceReadyMsg` literals without `InitialActive`: set `InitialActive: true` so the existing auto-claim path still runs.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: clean (no new warnings beyond what was there before this branch).

- [ ] **Step 3: Build**

Run: `make build`
Expected: success.

- [ ] **Step 4: Commit any test fixes**

```bash
git add -A
git commit -m "tests: update assertions broken by WorkspaceSwitchedMsg + InitialActive changes"
```

(Skip this commit if no test fixes were needed.)

---

### Task 22: Manual smoke test per spec checklist

**Files:**
- None modified.

This task validates the design against real Slack. It produces no code commits unless symptoms require follow-ups.

- [ ] **Step 1: Reproduce the original bug, confirm fix**

```bash
rm -f slk-debug.log
SLK_DEBUG=1 ./bin/slk
```

Drive the reproduction:

1. Wait for both workspaces to connect.
2. Switch to a channel in the non-default workspace (Ctrl+number, then j/k or Ctrl+T).
3. Switch to a channel in the other workspace.
4. Switch back to the first workspace.

Quit slk. Inspect the log:

```bash
grep -E '\[cache\]' slk-debug.log | grep -E 'tier=|channel_not_found|MessagesLoadedMsg'
```

Expected:
- No `channel_not_found` errors on either workspace.
- The return-trip channel-select reports `tier=1_fresh` (instant cache, no fetch) or `tier=2_verify` (cache + background fetch), not `tier=3_spinner`.
- No `older → newer` flicker visible in the UI during the return trip.

- [ ] **Step 2: Work through the spec's smoke-test checklist**

Open `docs/superpowers/specs/2026-05-10-messages-cache-integrity-design.md` to the "Smoke-test checklist" section. For each item:

- **Tier thresholds.** Try j/k cycling channels on a workspace with 20+ channels; do back-to-back revisits feel right? Try sitting on a workspace for 6+ minutes then re-entering a channel; does the Tier 3 spinner feel jarring? Note any adjustments.
- **`channelReadMarker` vs always-fire-fetcher.** Look at fetcher latencies in `slk-debug.log` (`fetchChannelMessages: ... dur_ms=N`). If most fetches are under ~200ms, consider folding back the dedicated marker callback and always firing the fetcher.
- **Indicator visibility timing.** Watch the `○` glyph during Tier 2 channel switches. Does it flash too briefly to register? Note whether a delay-before-show heuristic is worth adding.
- **`userResolver` parallelism.** Grep `slk-debug.log` for `userResolver: GetUserProfile ... err=`. Look for 429 / rate-limit responses. If present, propose a per-resolver semaphore as a follow-up.
- **Reconnect freshness.** Toggle wifi, wait 6 minutes, reconnect. First channel-select after reconnect should fall into Tier 3. Confirm and note any divergence.
- **`OnMessage`-driven `synced_at` bumps.** Tail the debug log while a busy channel receives messages. Confirm `OnMessage: SetChannelSyncedAt` entries appear and that a subsequent visit to that channel takes Tier 1.

- [ ] **Step 3: File follow-ups**

For each smoke-test finding that warrants a follow-up:

- Either fix in-place (small adjustment) and commit, OR
- Open a tracking issue / note in the spec's "Open questions" section for a future spec.

No commit if all smoke-test items pass cleanly.

---

## Plan complete

After Phase 5 passes:

- The bootstrap race no longer exists; `InitialActive` deterministically routes the auto-claim.
- The closure-rebinding `wireCallbacks` pattern is gone; one router pointer governs every workspace-scoped callback.
- Cache freshness is tracked per channel; the three-tier dispatch avoids redundant fetches in the fresh window and avoids stale renders in the very-old window.
- The visible "older → newer" flicker on workspace return is gone because async user resolution cuts post-fetch processing from ~2.5s to ~300ms (which falls below human perception when combined with Tier 1's no-fetch fast path).
- The `WorkspaceSwitchedMsg` handler no longer flashes an empty-pane-plus-spinner intermediate state.
