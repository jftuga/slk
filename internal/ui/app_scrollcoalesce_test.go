package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestScrollCoalesce_FirstMoveImmediateRestBatched pins the coalescing
// contract: the first j/k in a burst moves the selection immediately
// (instant feedback) and arms a flush tick; subsequent moves only
// accumulate (selection frozen) until the scrollFlushMsg tick applies
// the batch. This is what keeps a held key from outpacing rendering.
func TestScrollCoalesce_FirstMoveImmediateRestBatched(t *testing.T) {
	a := makeWideScrollApp()
	start := a.messagepane.SelectedIndex()

	// First k: immediate move + tick armed.
	if cmd := a.handleUp(); cmd == nil {
		t.Fatal("first move in a burst must return a cmd (immediate move + flush tick)")
	}
	if got := a.messagepane.SelectedIndex(); got != start-1 {
		t.Fatalf("first move must apply immediately: selected=%d want=%d", got, start-1)
	}
	if !a.scrollFlushScheduled {
		t.Fatal("first move must arm the flush tick")
	}

	// Next three k's only accumulate -- selection frozen.
	a.handleUp()
	a.handleUp()
	a.handleUp()
	if got := a.messagepane.SelectedIndex(); got != start-1 {
		t.Fatalf("subsequent moves must be deferred: selected=%d want=%d (frozen)", got, start-1)
	}
	if a.scrollPending != -3 {
		t.Fatalf("scrollPending=%d want=-3", a.scrollPending)
	}

	// Flush tick applies the batch. Because moves were pending, the tick
	// reschedules itself (scheduled stays true) so the expensive render
	// stays paced at the flush cadence during a continuous hold.
	_, _ = a.Update(scrollFlushMsg{})
	if got := a.messagepane.SelectedIndex(); got != start-4 {
		t.Fatalf("flush must apply batch: selected=%d want=%d", got, start-4)
	}
	if a.scrollPending != 0 {
		t.Fatalf("flush must drain pending: pending=%d", a.scrollPending)
	}
	if !a.scrollFlushScheduled {
		t.Fatal("flush that applied moves must reschedule (stay scheduled)")
	}

	// A subsequent tick with nothing pending ends the sequence.
	_, _ = a.Update(scrollFlushMsg{})
	if a.scrollFlushScheduled {
		t.Fatal("flush with no pending moves must clear the scheduled flag")
	}
	if got := a.messagepane.SelectedIndex(); got != start-4 {
		t.Fatalf("idle flush must not move selection: selected=%d want=%d", got, start-4)
	}
}

// TestScrollCoalesce_NonScrollKeyFlushesPending guards correctness: a key
// other than j/k must act on the up-to-date selection, so pending moves
// are drained before it dispatches. Without this, e.g. Enter would open
// the thread of a stale (pre-burst) message.
func TestScrollCoalesce_NonScrollKeyFlushesPending(t *testing.T) {
	a := makeWideScrollApp()
	start := a.messagepane.SelectedIndex()

	a.handleUp() // immediate -> start-1, tick armed
	a.handleUp() // accumulate -> pending -1
	a.handleUp() // accumulate -> pending -2
	if a.scrollPending != -2 {
		t.Fatalf("precondition: pending=%d want=-2", a.scrollPending)
	}

	// A non-scroll key (Right = focus change) routes through handleKey,
	// which must flush pending first.
	_ = a.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if a.scrollPending != 0 {
		t.Fatalf("non-scroll key must flush pending: pending=%d", a.scrollPending)
	}
	if got := a.messagepane.SelectedIndex(); got != start-3 {
		t.Fatalf("pending moves must be applied before the key acts: selected=%d want=%d", got, start-3)
	}
}

// TestScrollCoalesce_ClickDiscardsPending verifies a mouse click discards
// pending held-key moves (the click sets the selection absolutely, so the
// deferred moves must not fire later and yank the selection away).
func TestScrollCoalesce_ClickDiscardsPending(t *testing.T) {
	a := makeWideScrollApp()
	a.handleUp() // immediate + tick
	a.handleUp() // accumulate
	if a.scrollPending == 0 {
		t.Fatal("precondition: expected pending moves")
	}
	_, _ = a.Update(tea.MouseClickMsg{X: a.layout.sidebarEnd + 2, Y: 4, Button: tea.MouseLeft})
	if a.scrollPending != 0 {
		t.Fatalf("click must discard pending scroll: pending=%d", a.scrollPending)
	}
}
