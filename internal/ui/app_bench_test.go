package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/sidebar"
)

// makeBenchApp builds a populated App approximating a real user session: a
// sidebar with 100 channels, a message pane with 200 messages, a workspace
// rail with 3 workspaces, in INSERT mode with focus on the messages panel
// (the typical typing-in-compose state).
func makeBenchApp() *App {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 50})

	channels := make([]sidebar.ChannelItem, 100)
	for i := range channels {
		channels[i] = sidebar.ChannelItem{
			ID:   fmt.Sprintf("C%d", i),
			Name: fmt.Sprintf("channel-%d", i),
			Type: "channel",
		}
	}
	a.sidebar.SetItems(channels)

	msgs := make([]messages.MessageItem, 200)
	for i := range msgs {
		msgs[i] = messages.MessageItem{
			TS:        fmt.Sprintf("%d.0", 1700000000+i),
			UserName:  "alice",
			UserID:    "U1",
			Text:      "Hello world this is a moderately long message with **bold** and _italic_ formatting.",
			Timestamp: "10:30 AM",
		}
	}
	a.messagepane.SetMessages(msgs)
	a.activeChannelID = "C1"
	a.activeTeamID = "T1"

	a.SetMode(ModeInsert)
	a.focusedPanel = PanelMessages
	_, _ = a.compose.Focus(), a.compose.Focus() // ensure focused

	// Prime caches with one full render.
	_ = a.View()
	return a
}

// BenchmarkAppViewCompose measures the cost of one full top-level App.View()
// call after the user types a single character into the compose box. This
// is the steady-state user-typing cost: keystroke -> Update -> View.
func BenchmarkAppViewCompose(b *testing.B) {
	a := makeBenchApp()

	keyMsg := tea.KeyPressMsg{Code: 'a', Text: "a"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = a.Update(keyMsg)
		_ = a.View()
	}
}

// BenchmarkAppViewIdle measures the cost of one App.View() call when nothing
// at all changed since the last render. With ideal caching this should be
// O(1) -- if it isn't, we're doing avoidable work.
func BenchmarkAppViewIdle(b *testing.B) {
	a := makeBenchApp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.View()
	}
}

// makeWideScrollApp builds a NORMAL-mode app at an ultrawide terminal size
// (477x130, matching a real user's session in slk-debug.log) with focus on
// the messages pane -- the j/k scroll hot path. This is the case the
// compositor memo does NOT help: every scroll bumps messagepane.Version, so
// the bordered top region re-renders and the screen re-composites.
func makeWideScrollApp() *App {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 477, Height: 130})

	channels := make([]sidebar.ChannelItem, 100)
	for i := range channels {
		channels[i] = sidebar.ChannelItem{
			ID:   fmt.Sprintf("C%d", i),
			Name: fmt.Sprintf("channel-%d", i),
			Type: "channel",
		}
	}
	a.sidebar.SetItems(channels)

	msgs := make([]messages.MessageItem, 200)
	for i := range msgs {
		msgs[i] = messages.MessageItem{
			TS:        fmt.Sprintf("%d.0", 1700000000+i),
			UserName:  "alice",
			UserID:    "U1",
			Text:      "Hello world this is a moderately long message with **bold** and _italic_ formatting.",
			Timestamp: "10:30 AM",
		}
	}
	a.messagepane.SetMessages(msgs)
	a.activeChannelID = "C1"
	a.activeTeamID = "T1"
	a.focusedPanel = PanelMessages
	a.SetMode(ModeNormal)

	_ = a.View() // prime caches
	return a
}

// BenchmarkAppViewScroll measures the raw cost of a render that follows an
// actual selection change (the memo=false path): move the selection one
// row directly, then render. This is the irreducible per-changed-frame
// cost at ultrawide sizes.
func BenchmarkAppViewScroll(b *testing.B) {
	a := makeWideScrollApp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			a.messagepane.MoveDown()
		} else {
			a.messagepane.MoveUp()
		}
		_ = a.View()
	}
}

// BenchmarkAppViewScrollHeldKey simulates a fast key-repeat through the
// real Update->View path WITH coalescing: a burst of `down` keypresses
// punctuated by a flush tick every `flushEvery` events (the ~16ms cadence
// the event loop delivers scrollFlushMsg at). This is the user's "holding
// j" path. With coalescing, the non-first keypresses in each burst are
// Stage A memo hits, so the average per-event cost collapses -- which is
// what stops a held key from building a render backlog.
func BenchmarkAppViewScrollHeldKey(b *testing.B) {
	a := makeWideScrollApp()
	up := tea.KeyPressMsg{Code: tea.KeyUp} // k: genuine upward movement from the bottom

	// Roughly: a 200Hz key-repeat against a 16ms flush cadence yields
	// ~3 keypresses per flush. Model that ratio.
	const flushEvery = 3

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if a.messagepane.SelectedIndex() == 0 {
			a.messagepane.GoToBottom() // wrap so moves never stall at the top
		}
		_, _ = a.Update(up)
		_ = a.View()
		if i%flushEvery == flushEvery-1 {
			_, _ = a.Update(scrollFlushMsg{})
			_ = a.View()
		}
	}
}

// TestComposeKeystrokeKeepsSidePanelsCached verifies that typing a single
// character into the compose box does NOT bump the version counters of the
// sidebar / messages / workspace rail panels. Without this guarantee, the
// App-level panel cache would miss on every keystroke and rebuild work that
// the user can't see changing.
func TestComposeKeystrokeKeepsSidePanelsCached(t *testing.T) {
	a := makeBenchApp()
	_ = a.View() // prime caches

	sbBefore := a.sidebar.Version()
	msgBefore := a.messagepane.Version()
	railBefore := a.workspaceRail.Version()
	statusBefore := a.statusbar.Version()
	threadBefore := a.threadPanel.Version()

	keyMsg := tea.KeyPressMsg{Code: 'a', Text: "a"}
	_, _ = a.Update(keyMsg)
	_ = a.View()

	if v := a.sidebar.Version(); v != sbBefore {
		t.Errorf("sidebar.Version bumped on compose keystroke: before=%d after=%d", sbBefore, v)
	}
	if v := a.messagepane.Version(); v != msgBefore {
		t.Errorf("messagepane.Version bumped on compose keystroke: before=%d after=%d", msgBefore, v)
	}
	if v := a.workspaceRail.Version(); v != railBefore {
		t.Errorf("workspaceRail.Version bumped on compose keystroke: before=%d after=%d", railBefore, v)
	}
	if v := a.statusbar.Version(); v != statusBefore {
		t.Errorf("statusbar.Version bumped on compose keystroke: before=%d after=%d", statusBefore, v)
	}
	if v := a.threadPanel.Version(); v != threadBefore {
		t.Errorf("threadPanel.Version bumped on compose keystroke: before=%d after=%d", threadBefore, v)
	}

	// And compose IS expected to bump.
	if a.compose.Version() == 0 {
		t.Error("compose.Version did not bump on keystroke")
	}
}
