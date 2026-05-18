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

func TestOnChannelMarked_InactiveWorkspace_StillWritesDB(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	if err := db.UpdateChannelReadState("C1", "1.0000", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &rtmEventHandler{
		db:          db,
		wsCtx:       &WorkspaceContext{},
		isActive:    func() bool { return false }, // inactive workspace
		program:     nil,
		workspaceID: "T1",
	}
	h.OnChannelMarked("C1", "1.0050", 0)
	s, _ := db.GetChannelReadState("C1")
	if s.HasUnread {
		t.Errorf("HasUnread should be false even for inactive-workspace channel_marked")
	}
	if s.LastReadTS != "1.0050" {
		t.Errorf("LastReadTS = %q, want %q", s.LastReadTS, "1.0050")
	}
}

func TestMarkChannelReadAsync_UpdatesReadState(t *testing.T) {
	// markChannelReadAsync runs its work in a goroutine and calls
	// client.MarkChannel on a *slackclient.Client, which requires real
	// HTTP/Slack wiring (or a fake) to construct. The function body is
	// otherwise a thin wrapper over db.UpdateChannelReadState (covered by
	// cache-level tests) plus a tea.Program send. Wiring a fake Client
	// would require introducing an interface seam we don't otherwise need.
	// The reconnect-backfill integration test in Task 20 exercises this
	// path end-to-end.
	t.Skip("markChannelReadAsync requires a real *slackclient.Client; covered by Task 20 integration test")
}
