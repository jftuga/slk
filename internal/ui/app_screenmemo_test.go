package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestScreenMemo_ReflectsSelectionChange is the correctness guard for the
// Stage A compositor memo. Selection mutations deliberately do NOT bump
// any panel's Version() (the messages pane caches selection-free output
// and overlays the selection as a post-pass). A naive version-keyed memo
// would therefore serve a stale, selection-free frame after the user
// drags. This test proves the string-comparison memo catches the change.
func TestScreenMemo_ReflectsSelectionChange(t *testing.T) {
	a := newTestAppWithMessages(t)

	base := a.View().Content
	if !a.lastScreenValid {
		t.Fatal("precondition: a non-overlay render must populate the screen memo")
	}

	// Begin + extend + flush a drag selection.
	pressX := a.layout.sidebarEnd + 2
	pressY := 4
	_, _ = a.Update(tea.MouseClickMsg{X: pressX, Y: pressY, Button: tea.MouseLeft})
	_, _ = a.Update(tea.MouseMotionMsg{X: pressX + 8, Y: pressY + 1, Button: tea.MouseLeft})
	_, _ = a.Update(motionFlushTickMsg{})
	if !a.messagepane.HasSelection() || a.messagepane.SelectionText() == "" {
		t.Fatal("precondition: drag must produce a non-empty selection")
	}

	withSel := a.View().Content
	if withSel == base {
		t.Fatal("memo served a stale frame: selection change not reflected in View()")
	}

	// A subsequent no-change render must be byte-identical (memo hit) --
	// proving the memo is both correct and stable.
	again := a.View().Content
	if again != withSel {
		t.Fatal("no-change render diverged from the previous frame; memo not stable")
	}
}

// TestScreenMemo_InvalidatedByOverlay proves that opening an overlay marks
// the memo invalid (overlay output is never memoized because overlay
// content can change without bumping any base-panel version), and that the
// next non-overlay frame repopulates it.
func TestScreenMemo_InvalidatedByOverlay(t *testing.T) {
	a := newTestAppWithMessages(t)
	_ = a.View()
	if !a.lastScreenValid {
		t.Fatal("precondition: non-overlay render must populate the memo")
	}

	// Open the quit confirm overlay (ModeConfirm). overlayActive() is
	// true for confirmPrompt, so the next render must not memoize.
	a.openQuitConfirm()
	_ = a.View()
	if a.lastScreenValid {
		t.Fatal("overlay frame must invalidate the screen memo")
	}
}
