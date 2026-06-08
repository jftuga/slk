// Package reactionsview provides a read-only modal overlay that lists the
// reactions on a message, grouped by emoji, with the display names of the
// users who reacted with it. Data is supplied by the App (assembled from the
// cached per-user reaction data); the modal does not fetch anything itself.
package reactionsview

// ReactionGroup is one emoji and the resolved display names of the users who
// reacted with it. The current user's name is expected to already carry a
// "(you)" suffix when assembled by the caller.
type ReactionGroup struct {
	Emoji string
	Users []string
}

// Model is the reactions-list overlay state.
type Model struct {
	groups  []ReactionGroup
	visible bool
	offset  int // scroll offset in rendered content lines
	maxOff  int // last computed maximum offset (set during render)
}

// New creates an empty, hidden modal.
func New() *Model { return &Model{} }

// Open shows the modal for the given reaction groups and resets scroll.
func (m *Model) Open(groups []ReactionGroup) {
	m.groups = groups
	m.offset = 0
	m.maxOff = 0
	m.visible = true
}

// Close hides the modal and clears state.
func (m *Model) Close() {
	m.visible = false
	m.groups = nil
	m.offset = 0
	m.maxOff = 0
}

// IsVisible reports whether the modal is showing.
func (m *Model) IsVisible() bool { return m.visible }

// Offset returns the current scroll offset (exported for tests).
func (m *Model) Offset() int { return m.offset }

// HandleKey processes a key for the modal. esc/q/L closes it; up/down and j/k
// scroll. Scroll is clamped to [0, maxOff], where maxOff is recomputed on each
// render; before the first render maxOff is 0 so scrolling is inert.
func (m *Model) HandleKey(keyStr string) {
	switch keyStr {
	case "esc", "escape", "q", "L":
		m.Close()
	case "up", "k":
		if m.offset > 0 {
			m.offset--
		}
	case "down", "j":
		if m.offset < m.maxOff {
			m.offset++
		}
	}
}
