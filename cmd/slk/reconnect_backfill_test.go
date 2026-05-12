package main

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gammons/slk/internal/cache"
	"github.com/gammons/slk/internal/ui"
	"github.com/slack-go/slack"
)

// fakeHistory implements historyFetcher for backfill tests. Tracks
// call count per channel and peak concurrency. repliesResponses /
// repliesCalls support the thread-phase tests.
type fakeHistory struct {
	mu               sync.Mutex
	inFlight         int32
	maxInFlight      int32
	delay            time.Duration
	responses        map[string][]*slack.GetConversationHistoryResponse
	calls            map[string]int
	oldestSeen       map[string][]string // per-channel: oldest param of each call, in order
	repliesResponses map[string][]slack.Message // keyed by threadTS
	repliesCalls     []struct{ Channel, TS string }
}

// GetHistorySince satisfies historyFetcher. It looks up the per-channel
// response queue, returns its head, and records the call.
func (f *fakeHistory) GetHistorySince(ctx context.Context, channelID, oldest string, maxTotal int) ([]slack.Message, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		hi := atomic.LoadInt32(&f.maxInFlight)
		if cur <= hi || atomic.CompareAndSwapInt32(&f.maxInFlight, hi, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[channelID]++
	f.oldestSeen[channelID] = append(f.oldestSeen[channelID], oldest)
	resps := f.responses[channelID]
	if len(resps) == 0 {
		return nil, nil
	}
	resp := resps[0]
	f.responses[channelID] = resps[1:]
	return resp.Messages, nil
}

// GetReplies satisfies historyFetcher. Records the call and returns
// the preconfigured reply slice for the given threadTS, if any.
func (f *fakeHistory) GetReplies(ctx context.Context, channelID, threadTS string) ([]slack.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repliesCalls = append(f.repliesCalls, struct{ Channel, TS string }{channelID, threadTS})
	if replies, ok := f.repliesResponses[threadTS]; ok {
		return replies, nil
	}
	return nil, nil
}

// captureSender records every tea.Msg dispatched to it. Substituted
// for *tea.Program in tests via the teaSender interface.
type captureSender struct {
	mu   sync.Mutex
	sent []tea.Msg
}

func (c *captureSender) Send(msg tea.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
}

func newTestDB(t *testing.T) *cache.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := cache.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.UpsertWorkspace(cache.Workspace{ID: "T1", Name: "T"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	return db
}

func TestBackfillChannels_FetchesPerChannelSinceSyncedAt(t *testing.T) {
	db := newTestDB(t)

	// Two channels with cached messages and synced_at.
	db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "a", Type: "channel"})
	db.UpsertChannel(cache.Channel{ID: "C2", WorkspaceID: "T1", Name: "b", Type: "channel"})
	db.UpsertMessage(cache.Message{TS: "10.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U1", Text: "old"})
	db.UpsertMessage(cache.Message{TS: "20.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U1", Text: "old"})
	db.SetChannelSyncedAt("C1", 100)
	db.SetChannelSyncedAt("C2", 200)

	fh := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{
			"C1": {{Messages: []slack.Message{{Msg: slack.Msg{Timestamp: "150.000000", User: "U2", Text: "new in c1"}}}}},
			"C2": {{Messages: []slack.Message{{Msg: slack.Msg{Timestamp: "250.000000", User: "U2", Text: "new in c2"}}}}},
		},
		calls:      map[string]int{},
		oldestSeen: map[string][]string{},
	}

	bf := newBackfiller(fh, db, "T1", "USELF", nil, 4, 500)
	if err := bf.runChannelPhase(context.Background()); err != nil {
		t.Fatalf("runChannelPhase: %v", err)
	}

	if fh.calls["C1"] != 1 || fh.calls["C2"] != 1 {
		t.Errorf("expected 1 call each for C1 and C2, got %+v", fh.calls)
	}
	if got := fh.oldestSeen["C1"]; len(got) != 1 || got[0] != "100.000000" {
		t.Errorf("C1 oldest = %+v, want [100.000000]", got)
	}
	if got := fh.oldestSeen["C2"]; len(got) != 1 || got[0] != "200.000000" {
		t.Errorf("C2 oldest = %+v, want [200.000000]", got)
	}
	// New messages were upserted.
	if _, err := db.GetMessage("C1", "150.000000"); err != nil {
		t.Errorf("missing upserted message C1/150: %v", err)
	}
	if _, err := db.GetMessage("C2", "250.000000"); err != nil {
		t.Errorf("missing upserted message C2/250: %v", err)
	}
}

func TestBackfillChannels_BoundedConcurrency(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 8; i++ {
		id := "C" + string(rune('1'+i))
		db.UpsertChannel(cache.Channel{ID: id, WorkspaceID: "T1", Name: id, Type: "channel"})
		db.UpsertMessage(cache.Message{TS: "1.000000", ChannelID: id, WorkspaceID: "T1", UserID: "U", Text: "x"})
	}

	responses := map[string][]*slack.GetConversationHistoryResponse{}
	for i := 0; i < 8; i++ {
		id := "C" + string(rune('1'+i))
		responses[id] = []*slack.GetConversationHistoryResponse{{}}
	}
	fh := &fakeHistory{
		delay:      50 * time.Millisecond,
		responses:  responses,
		calls:      map[string]int{},
		oldestSeen: map[string][]string{},
	}

	bf := newBackfiller(fh, db, "T1", "USELF", nil, 4, 500)
	if err := bf.runChannelPhase(context.Background()); err != nil {
		t.Fatalf("runChannelPhase: %v", err)
	}

	if got := atomic.LoadInt32(&fh.maxInFlight); got > 4 {
		t.Errorf("max in-flight = %d, want <= 4", got)
	}
	if len(fh.calls) != 8 {
		t.Errorf("expected 8 channels called, got %d", len(fh.calls))
	}
}

// TestBackfillThreads_FetchesRepliesForInvolvedThreads verifies that
// after the channel phase populates discoveredThreads, the thread
// phase filters to threads where the cache shows the user is involved
// (parent or any reply authored by selfUserID) and fetches replies
// only for those.
func TestBackfillThreads_FetchesRepliesForInvolvedThreads(t *testing.T) {
	db := newTestDB(t)
	db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "a", Type: "channel"})
	// Existing cached parent in thread 100: self authored → involved.
	db.UpsertMessage(cache.Message{TS: "100.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "self parent", ThreadTS: "100.000000"})
	// Existing cached parent in thread 200: not involved.
	db.UpsertMessage(cache.Message{TS: "200.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "other parent", ThreadTS: "200.000000"})
	db.SetChannelSyncedAt("C1", 50)

	fh := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{
			"C1": {{Messages: []slack.Message{
				// New reply on involved thread 100.
				{Msg: slack.Msg{Timestamp: "150.000000", User: "U2", Text: "reply to self", ThreadTimestamp: "100.000000"}},
				// New reply on non-involved thread 200.
				{Msg: slack.Msg{Timestamp: "250.000000", User: "U3", Text: "reply on other", ThreadTimestamp: "200.000000"}},
			}}},
		},
		calls:      map[string]int{},
		oldestSeen: map[string][]string{},
		repliesResponses: map[string][]slack.Message{
			"100.000000": {
				{Msg: slack.Msg{Timestamp: "100.000000", User: "USELF", Text: "self parent", ThreadTimestamp: "100.000000"}},
				{Msg: slack.Msg{Timestamp: "150.000000", User: "U2", Text: "reply to self", ThreadTimestamp: "100.000000"}},
			},
		},
	}

	bf := newBackfiller(fh, db, "T1", "USELF", nil, 4, 500)
	if err := bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(fh.repliesCalls) != 1 {
		t.Fatalf("expected 1 replies call (for involved thread 100), got %d: %+v", len(fh.repliesCalls), fh.repliesCalls)
	}
	if fh.repliesCalls[0].Channel != "C1" || fh.repliesCalls[0].TS != "100.000000" {
		t.Errorf("replies call = %+v, want C1/100.000000", fh.repliesCalls[0])
	}
}

// TestBackfill_FiresThreadsListDirtyMsg verifies that run() dispatches
// exactly one ThreadsListDirtyMsg into the program, carrying the
// workspace ID so the UI knows which team's threads view to re-query.
func TestBackfill_FiresThreadsListDirtyMsg(t *testing.T) {
	db := newTestDB(t)
	db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "a", Type: "channel"})
	db.UpsertMessage(cache.Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U", Text: "x"})
	db.SetChannelSyncedAt("C1", 100)

	fh := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{
			"C1": {{}},
		},
		calls:      map[string]int{},
		oldestSeen: map[string][]string{},
	}

	captured := &captureSender{}
	bf := newBackfiller(fh, db, "T1", "USELF", captured, 4, 500)
	if err := bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	captured.mu.Lock()
	defer captured.mu.Unlock()
	if len(captured.sent) != 1 {
		t.Fatalf("expected 1 sent msg, got %d", len(captured.sent))
	}
	dirty, ok := captured.sent[0].(ui.ThreadsListDirtyMsg)
	if !ok {
		t.Fatalf("expected ThreadsListDirtyMsg, got %T", captured.sent[0])
	}
	if dirty.TeamID != "T1" {
		t.Errorf("TeamID = %s, want T1", dirty.TeamID)
	}
}

func TestBackfill_DedupeWindow(t *testing.T) {
	gate := &dedupeGate{window: 30 * time.Second}

	first := gate.tryStart(time.Unix(1000, 0))
	if !first {
		t.Fatal("first call should be allowed")
	}
	second := gate.tryStart(time.Unix(1010, 0))
	if second {
		t.Error("second call within 30s should be blocked")
	}
	third := gate.tryStart(time.Unix(1031, 0))
	if !third {
		t.Error("call after window should be allowed")
	}
}
