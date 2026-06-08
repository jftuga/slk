package reactionsview

import "testing"

func sampleGroups() []ReactionGroup {
	return []ReactionGroup{
		{Emoji: "thumbsup", Users: []string{"Alice", "Bob", "You (you)"}},
		{Emoji: "eyes", Users: []string{"Carol"}},
	}
}

func TestOpenCloseVisibility(t *testing.T) {
	m := New()
	if m.IsVisible() {
		t.Fatal("new model should not be visible")
	}
	m.Open(sampleGroups())
	if !m.IsVisible() {
		t.Fatal("Open should make the model visible")
	}
	m.Close()
	if m.IsVisible() {
		t.Fatal("Close should hide the model")
	}
}

func TestHandleKeyEscapeCloses(t *testing.T) {
	m := New()
	m.Open(sampleGroups())
	m.HandleKey("esc")
	if m.IsVisible() {
		t.Fatal("esc should close the modal")
	}
}

func TestHandleKeyScrollClamps(t *testing.T) {
	m := New()
	m.Open(sampleGroups())
	// Scrolling up at the top stays at 0.
	m.HandleKey("up")
	if m.Offset() != 0 {
		t.Fatalf("offset should clamp to 0 at top, got %d", m.Offset())
	}
	// Many downs should not exceed the max offset (never negative).
	for i := 0; i < 100; i++ {
		m.HandleKey("down")
	}
	if m.Offset() < 0 {
		t.Fatalf("offset should never be negative, got %d", m.Offset())
	}
}
