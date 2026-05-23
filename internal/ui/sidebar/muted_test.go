package sidebar

import (
	"strings"
	"testing"

	"github.com/gammons/slk/internal/cache"
)

// The unread blue dot is "●" (U+25CF). Muted-with-unreads channels
// must NOT carry it — Slack's contract is "muted = no notification
// surface". The dimmer ChannelMuted style is what tells the user a
// muted row has activity, not the dot.
func TestMutedChannel_SuppressesUnreadDot(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "noisy", Type: "channel", IsMuted: true},
	})
	m.SetReadStateReader(func() map[string]cache.ReadState {
		return map[string]cache.ReadState{"C1": {HasUnread: true}}
	})
	m.ToggleCollapse("Channels") // expand so the row renders
	view := m.View(10, 30)

	var line string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "noisy") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("noisy row not rendered:\n%s", view)
	}
	if strings.Contains(line, "●") {
		t.Errorf("muted channel rendered an unread dot:\n%q", line)
	}
}

// Sanity: an unmuted channel with unreads *does* get the dot. Guards
// against the suppression accidentally firing for every row.
func TestUnmutedChannel_StillGetsUnreadDot(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "noisy", Type: "channel", IsMuted: false},
	})
	m.SetReadStateReader(func() map[string]cache.ReadState {
		return map[string]cache.ReadState{"C1": {HasUnread: true}}
	})
	m.ToggleCollapse("Channels")
	view := m.View(10, 30)

	var line string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "noisy") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("noisy row not rendered:\n%s", view)
	}
	if !strings.Contains(line, "●") {
		t.Errorf("unmuted channel with unreads is missing the dot:\n%q", line)
	}
}

// Aggregate badge on a collapsed section header counts
// channels-with-unreads. Muted channels are explicitly excluded so the
// badge matches the per-row treatment (no dot, dim foreground).
func TestAggregateBadge_ExcludesMutedChannels(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "general", Type: "channel", IsMuted: false},
		{ID: "C2", Name: "noisy", Type: "channel", IsMuted: true},
		{ID: "C3", Name: "alerts", Type: "channel", IsMuted: false},
	})
	m.SetReadStateReader(func() map[string]cache.ReadState {
		return map[string]cache.ReadState{
			"C1": {HasUnread: true},
			"C2": {HasUnread: true}, // muted: must not count
			"C3": {HasUnread: true},
		}
	})
	// Collapsed by default; aggregate counts 2 channels-with-unreads
	// (C1 and C3); the muted C2 must not contribute.
	view := m.View(15, 30)
	if !strings.Contains(view, "•2") {
		t.Errorf("expected aggregate badge •2 (muted excluded), got:\n%s", view)
	}
	if strings.Contains(view, "•3") {
		t.Errorf("aggregate badge counted muted channel toward total:\n%s", view)
	}
}

// When every channel in a section is muted, the collapsed header's
// aggregate badge should disappear entirely. Guards against rendering
// a stray "•0" (or worse, the unmuted code path).
func TestAggregateBadge_AllMutedDropsBadge(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "noisy", Type: "channel", IsMuted: true},
		{ID: "C2", Name: "spammy", Type: "channel", IsMuted: true},
	})
	m.SetReadStateReader(func() map[string]cache.ReadState {
		return map[string]cache.ReadState{
			"C1": {HasUnread: true},
			"C2": {HasUnread: true},
		}
	})
	view := m.View(15, 30)
	// Find the Channels header line.
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "Channels") {
			if strings.Contains(l, "•") {
				t.Errorf("collapsed header showed a badge despite every channel being muted:\n%q", l)
			}
			return
		}
	}
}

// TestChannelItem_IsVisiblyUnread locks in the single predicate that
// drives the sidebar dot, the section aggregate, AND the tab-title
// count. If a future change diverges any of those call sites from the
// predicate captured here, the corresponding render/count tests will
// fail -- but this one fails first and points directly at the rule.
func TestChannelItem_IsVisiblyUnread(t *testing.T) {
	cases := []struct {
		name  string
		item  ChannelItem
		state cache.ReadState
		want  bool
	}{
		{"unread, unmuted", ChannelItem{IsMuted: false}, cache.ReadState{HasUnread: true}, true},
		{"unread, muted", ChannelItem{IsMuted: true}, cache.ReadState{HasUnread: true}, false},
		{"read, unmuted", ChannelItem{IsMuted: false}, cache.ReadState{HasUnread: false}, false},
		{"read, muted", ChannelItem{IsMuted: true}, cache.ReadState{HasUnread: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.IsVisiblyUnread(tc.state); got != tc.want {
				t.Errorf("IsVisiblyUnread = %v want %v", got, tc.want)
			}
		})
	}
}

func TestUnreadChannelCount_NoReader(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "general", Type: "channel"},
	})
	if got := m.UnreadChannelCount(); got != 0 {
		t.Errorf("UnreadChannelCount with no reader = %d want 0", got)
	}
}

// TestUnreadChannelCount_FiltersMute proves the count uses
// IsVisiblyUnread -- muted-but-unread channels are excluded, matching
// the dot population. If this assertion ever drifts from the sidebar
// dot count, the predicate has been duplicated somewhere.
func TestUnreadChannelCount_FiltersMute(t *testing.T) {
	m := New([]ChannelItem{
		{ID: "C1", Name: "a", Type: "channel", IsMuted: false},
		{ID: "C2", Name: "b", Type: "channel", IsMuted: false},
		{ID: "C3", Name: "c", Type: "channel", IsMuted: true},
		{ID: "C4", Name: "d", Type: "channel", IsMuted: false},
	})
	m.SetReadStateReader(func() map[string]cache.ReadState {
		return map[string]cache.ReadState{
			"C1": {HasUnread: true},  // counted
			"C2": {HasUnread: false}, // skipped (read)
			"C3": {HasUnread: true},  // skipped (muted)
			"C4": {HasUnread: true},  // counted
		}
	})
	if got := m.UnreadChannelCount(); got != 2 {
		t.Errorf("UnreadChannelCount = %d want 2 (C1, C4)", got)
	}
}

func TestUnreadChannelCount_EmptySidebar(t *testing.T) {
	m := New(nil)
	m.SetReadStateReader(func() map[string]cache.ReadState { return nil })
	if got := m.UnreadChannelCount(); got != 0 {
		t.Errorf("UnreadChannelCount with no items = %d want 0", got)
	}
}
