// Package newmessagepicker is the Ctrl+N modal that lets the user
// start a DM (one recipient) or group DM / MPIM (2-8 recipients).
//
// The model is a self-contained Bubble Tea sub-model mirroring the
// channelfinder package: a fuzzy filter over a user list with a
// pill-bar multi-select layered on top. Submission returns a Result
// carrying the chosen user IDs; the caller (the App's mode handler)
// is responsible for dispatching the conversations.open call.
package newmessagepicker

// MaxRecipients is Slack's hard cap on the number of OTHER users in
// a multi-person direct message (MPIM). Slack itself caps total
// MPIM participants at 9, so up to 8 other users plus self.
const MaxRecipients = 8

// User is one row in the picker's list. DisplayName is the
// human-friendly name shown to the user; Username is the Slack handle
// (without the leading @). Recency is the unix-second timestamp of
// the most recent activity tied to this user; higher values sort
// earlier under empty-query and break ties under a query. IsExternal
// drives the [ext] tag in the rendered row.
type User struct {
	ID          string
	DisplayName string
	Username    string
	IsExternal  bool
	Recency     int64
}

// Result is returned by HandleKey when the user submits the picker.
// UserIDs is the list of recipients to pass to conversations.open;
// it always contains at least one ID when non-nil.
type Result struct {
	UserIDs []string
}

// Model is the picker's state. Constructed with New() and held on the
// App while ModeNewMessage is active.
type Model struct {
	users         []User
	filtered      []int // indices into users matching query + not-self
	query         string
	selected      map[string]struct{} // user IDs in the pill bar
	highlight     int                 // index into filtered
	visible       bool
	currentUserID string
}

// New constructs an empty picker. SetUsers and (optionally)
// SetCurrentUserID should be called before Open.
func New() Model {
	return Model{
		selected: map[string]struct{}{},
	}
}

// SetUsers replaces the user list the picker filters over.
// Does not trigger a re-filter; that happens on Open.
func (m *Model) SetUsers(users []User) {
	m.users = users
}

// SetCurrentUserID configures which user ID represents "self" so the
// picker can hide it from the list (a user cannot start a DM with
// themselves via this flow). May be called once at app start.
func (m *Model) SetCurrentUserID(userID string) {
	m.currentUserID = userID
}

// Open shows the picker and resets per-session state: query, pill
// selection, highlight, and recomputes the filtered list.
func (m *Model) Open() {
	m.visible = true
	m.query = ""
	m.selected = map[string]struct{}{}
	m.highlight = 0
	m.filter()
}

// Close hides the picker. Does not clear users; Open will re-filter.
func (m *Model) Close() {
	m.visible = false
}

// IsVisible reports whether the picker is currently open.
func (m Model) IsVisible() bool {
	return m.visible
}

// setQuery is a test helper: replaces the query and refilters. The
// real keystroke API in Task 4 will go through HandleKey.
func (m *Model) setQuery(q string) {
	m.query = q
	m.highlight = 0
	m.filter()
}
