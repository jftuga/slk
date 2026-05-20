package membership

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gammons/slk/internal/cache"
)

// fakeMemberAPI implements ConversationMemberAPI for tests.
type fakeMemberAPI struct {
	mu     sync.Mutex
	calls  int
	result []string
	err    error
}

func (f *fakeMemberAPI) GetUsersInConversation(ctx context.Context, channelID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeMemberAPI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// captureSink records ChannelMembershipMsg pushes.
type captureSink struct {
	mu     sync.Mutex
	pushes []capturedPush
}
type capturedPush struct {
	channelID string
	memberIDs []string
}

func (s *captureSink) Push(channelID string, memberIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(memberIDs))
	copy(cp, memberIDs)
	s.pushes = append(s.pushes, capturedPush{channelID, cp})
}
func (s *captureSink) snapshot() []capturedPush {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedPush, len(s.pushes))
	copy(out, s.pushes)
	return out
}

func newManagerForTest(t *testing.T) (*Manager, *fakeMemberAPI, *captureSink, *cache.DB) {
	t.Helper()
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.UpsertWorkspace(cache.Workspace{ID: "T1", Name: "Test"})
	api := &fakeMemberAPI{}
	sink := &captureSink{}
	mgr := New("T1", api, db, sink.Push, nil /* userResolver */)
	return mgr, api, sink, db
}

func TestEnsureFreshCacheHitNoFetch(t *testing.T) {
	mgr, api, sink, db := newManagerForTest(t)
	defer db.Close()
	// Seed cache with recent meta.
	_ = db.ReplaceChannelMembers("T1", "C1", []string{"U1", "U2"}, time.Now().Unix())

	mgr.EnsureFresh(context.Background(), "C1")
	// EnsureFresh kicks off background work; wait for any push.
	waitForPush(t, sink, 1)

	if api.callCount() != 0 {
		t.Errorf("fresh cache should NOT trigger fetch; got %d calls", api.callCount())
	}
	pushes := sink.snapshot()
	if len(pushes) != 1 || pushes[0].channelID != "C1" {
		t.Errorf("expected 1 push for C1; got %+v", pushes)
	}
}

func TestEnsureFreshCacheMissTriggersFetch(t *testing.T) {
	mgr, api, sink, db := newManagerForTest(t)
	defer db.Close()
	api.result = []string{"U1", "U2", "U3"}

	mgr.EnsureFresh(context.Background(), "C1")
	waitForPush(t, sink, 1)     // initial empty push
	waitForCallCount(t, api, 1) // fetch happens
	waitForPush(t, sink, 2)     // post-fetch push

	if api.callCount() != 1 {
		t.Errorf("expected 1 fetch call; got %d", api.callCount())
	}
	pushes := sink.snapshot()
	if len(pushes) < 2 {
		t.Fatalf("expected >=2 pushes; got %d", len(pushes))
	}
	last := pushes[len(pushes)-1]
	if len(last.memberIDs) != 3 {
		t.Errorf("final push had %d members; want 3", len(last.memberIDs))
	}

	// Cache persisted?
	got, _ := db.ListChannelMembers("T1", "C1")
	if len(got) != 3 {
		t.Errorf("expected 3 cached members; got %d", len(got))
	}
}

func TestEnsureFreshStaleTriggersFetch(t *testing.T) {
	mgr, api, sink, db := newManagerForTest(t)
	defer db.Close()
	// Seed cache as stale (yesterday).
	stale := time.Now().Add(-25 * time.Hour).Unix()
	_ = db.ReplaceChannelMembers("T1", "C1", []string{"U1"}, stale)
	api.result = []string{"U1", "U2"}

	mgr.EnsureFresh(context.Background(), "C1")
	waitForPush(t, sink, 1)
	waitForCallCount(t, api, 1)
	waitForPush(t, sink, 2)

	if api.callCount() != 1 {
		t.Errorf("stale cache should trigger fetch; got %d calls", api.callCount())
	}
}

// Helpers — poll briefly because the Manager fans work to goroutines.
func waitForPush(t *testing.T, s *captureSink, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(s.snapshot()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pushes; got %d", n, len(s.snapshot()))
}
func waitForCallCount(t *testing.T, api *fakeMemberAPI, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if api.callCount() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d API calls", n)
}
