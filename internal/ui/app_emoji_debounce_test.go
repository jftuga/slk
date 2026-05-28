package ui

import (
	"testing"
)

// TestEmojiImageReady_Debounces guards the coalescing behavior of the
// EmojiImageReadyMsg reducer arm. On a busy channel with many cold-cache
// emoji, fetch completions arrive in a burst (one EmojiImageReadyMsg per
// unique emoji). The naive handling — invalidate every emoji-rendering
// surface's cache on every arrival — produces a cascade of full cache
// rebuilds, saturates the UI thread, and looks like a freeze.
//
// The reducer debounces: the first arrival schedules a tick, subsequent
// arrivals within the window are absorbed into the pending batch and
// return no additional Cmd. When the tick fires (emojiInvalidateMsg) a
// single wholesale invalidation runs across all surfaces.
//
// Asserts: first arrival returns a non-nil Cmd (the tea.Tick), second
// arrival within the window returns nil (absorbed).
func TestEmojiImageReady_Debounces(t *testing.T) {
	a := NewApp()
	a.width = 120
	a.height = 30

	// First arrival should schedule a tick. Update returns (Model, Cmd);
	// the Cmd is the debounce tick.
	_, cmd1 := a.Update(EmojiImageReadyMsg{URL: "https://x/a.png"})
	if cmd1 == nil {
		t.Fatal("first EmojiImageReadyMsg should return a tick cmd (debounce timer)")
	}
	if !a.emojiInvalidatePending {
		t.Errorf("emojiInvalidatePending should be true after first EmojiImageReadyMsg")
	}

	// Second arrival within the debounce window should be absorbed —
	// no additional tick scheduled.
	_, cmd2 := a.Update(EmojiImageReadyMsg{URL: "https://x/b.png"})
	if cmd2 != nil {
		t.Errorf("second EmojiImageReadyMsg should be absorbed into pending batch (cmd should be nil), got non-nil cmd")
	}
	if !a.emojiInvalidatePending {
		t.Errorf("emojiInvalidatePending should remain true while window is open")
	}

	// Synthesize the debounce-tick payload and feed it back in. The
	// reducer should clear the pending flag and (we don't directly
	// observe the inner HandleEmojiImageReady calls, but) leave the
	// system ready for the next round.
	_, _ = a.Update(emojiInvalidateMsg{})
	if a.emojiInvalidatePending {
		t.Errorf("emojiInvalidatePending should be false after emojiInvalidateMsg processed")
	}

	// After the window closes, the next arrival should schedule a fresh
	// tick — debounce is not a one-shot.
	_, cmd3 := a.Update(EmojiImageReadyMsg{URL: "https://x/c.png"})
	if cmd3 == nil {
		t.Fatal("post-window EmojiImageReadyMsg should schedule a fresh tick")
	}
}
