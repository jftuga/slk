package newmessagepicker

import "testing"

func testUsers() []User {
	return []User{
		{ID: "U1", DisplayName: "Alice Chen", Username: "alice", Recency: 500},
		{ID: "U2", DisplayName: "Bob Singh", Username: "bob", Recency: 400},
		{ID: "U3", DisplayName: "Carla Diaz", Username: "carla", Recency: 300},
		{ID: "U4", DisplayName: "Dan Evans", Username: "dan", Recency: 200},
		{ID: "U5", DisplayName: "Eva Frank", Username: "eva", IsExternal: true, Recency: 100},
	}
}

func TestNew_NotVisibleByDefault(t *testing.T) {
	m := New()
	if m.IsVisible() {
		t.Error("expected new model to not be visible")
	}
}

func TestOpen_MakesVisible(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	if !m.IsVisible() {
		t.Error("expected Open() to make model visible")
	}
}

func TestClose_HidesModel(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	m.Close()
	if m.IsVisible() {
		t.Error("expected Close() to hide model")
	}
}

func TestOpen_ResetsState(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	// Simulate dirty state from a previous session.
	m.query = "old query"
	m.selected["U1"] = struct{}{}
	m.highlight = 3

	m.Close()
	m.Open()

	if m.query != "" {
		t.Errorf("expected empty query after Open, got %q", m.query)
	}
	if len(m.selected) != 0 {
		t.Errorf("expected empty selection after Open, got %d entries", len(m.selected))
	}
	if m.highlight != 0 {
		t.Errorf("expected highlight=0 after Open, got %d", m.highlight)
	}
}

func TestSetCurrentUserID_ExcludesSelfFromList(t *testing.T) {
	users := testUsers()
	m := New()
	m.SetCurrentUserID("U2") // Bob is "self"
	m.SetUsers(users)
	m.Open()

	for _, idx := range m.filtered {
		if m.users[idx].ID == "U2" {
			t.Error("self user U2 should not appear in filtered list")
		}
	}
}

func TestFilter_EmptyQuerySortsByRecencyDesc(t *testing.T) {
	m := New()
	m.SetUsers(testUsers()) // Alice=500, Bob=400, Carla=300, Dan=200, Eva=100
	m.Open()

	if len(m.filtered) != 5 {
		t.Fatalf("expected 5 users, got %d", len(m.filtered))
	}
	wantOrder := []string{"U1", "U2", "U3", "U4", "U5"}
	for i, want := range wantOrder {
		got := m.users[m.filtered[i]].ID
		if got != want {
			t.Errorf("position %d: want %s, got %s", i, want, got)
		}
	}
}

func TestFilter_EmptyQueryTieBreaksAlphabetically(t *testing.T) {
	users := []User{
		{ID: "U1", DisplayName: "Charlie", Username: "c", Recency: 100},
		{ID: "U2", DisplayName: "Alice", Username: "a", Recency: 100},
		{ID: "U3", DisplayName: "Bob", Username: "b", Recency: 100},
	}
	m := New()
	m.SetUsers(users)
	m.Open()

	wantOrder := []string{"U2", "U3", "U1"} // Alice, Bob, Charlie
	for i, want := range wantOrder {
		got := m.users[m.filtered[i]].ID
		if got != want {
			t.Errorf("position %d: want %s, got %s", i, want, got)
		}
	}
}

func TestFilter_PrefixBeatsSubstring(t *testing.T) {
	users := []User{
		{ID: "U1", DisplayName: "Marcus", Username: "marcus", Recency: 100},
		{ID: "U2", DisplayName: "Alice Marketing", Username: "amark", Recency: 999},
	}
	m := New()
	m.SetUsers(users)
	m.Open()
	m.setQuery("mar")

	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(m.filtered))
	}
	if m.users[m.filtered[0]].ID != "U1" {
		t.Errorf("prefix match should come first, got %s", m.users[m.filtered[0]].ID)
	}
}

func TestFilter_SubstringBeatsSubsequence(t *testing.T) {
	users := []User{
		{ID: "U1", DisplayName: "Stephanie", Username: "steph", Recency: 100},   // contains "eph"
		{ID: "U2", DisplayName: "Edward Phillips", Username: "ep", Recency: 999}, // subseq e-p-h
	}
	m := New()
	m.SetUsers(users)
	m.Open()
	m.setQuery("eph")

	if m.filtered[0] != 0 {
		t.Errorf("substring match should rank first, got user at index %d", m.filtered[0])
	}
}

func TestFilter_CaseInsensitive(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	m.setQuery("ALICE")

	if len(m.filtered) == 0 {
		t.Fatal("expected at least 1 match for ALICE")
	}
	if m.users[m.filtered[0]].ID != "U1" {
		t.Errorf("expected Alice (U1) as first match, got %s", m.users[m.filtered[0]].ID)
	}
}

func TestFilter_MatchesUsernameHandle(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	m.setQuery("dan") // Dan Evans has Username="dan"

	if len(m.filtered) == 0 {
		t.Fatal("expected match for handle 'dan'")
	}
	if m.users[m.filtered[0]].ID != "U4" {
		t.Errorf("expected Dan (U4) as first match, got %s", m.users[m.filtered[0]].ID)
	}
}

func TestFilter_NoMatchesReturnsEmpty(t *testing.T) {
	m := New()
	m.SetUsers(testUsers())
	m.Open()
	m.setQuery("xyzqq")

	if len(m.filtered) != 0 {
		t.Errorf("expected 0 matches for unmatchable query, got %d", len(m.filtered))
	}
}

func TestFilter_ExcludesSelfEvenOnMatch(t *testing.T) {
	users := testUsers()
	m := New()
	m.SetCurrentUserID("U1") // Alice is self
	m.SetUsers(users)
	m.Open()
	m.setQuery("alice")

	for _, idx := range m.filtered {
		if m.users[idx].ID == "U1" {
			t.Error("self user should be excluded even when query matches")
		}
	}
}

func TestFilter_RecencyTieBreaksWithinSameTier(t *testing.T) {
	users := []User{
		{ID: "U1", DisplayName: "alice older", Username: "a1", Recency: 100},
		{ID: "U2", DisplayName: "alice newer", Username: "a2", Recency: 999},
	}
	m := New()
	m.SetUsers(users)
	m.Open()
	m.setQuery("alice") // both prefix-match

	if m.users[m.filtered[0]].ID != "U2" {
		t.Errorf("higher-recency match should come first, got %s", m.users[m.filtered[0]].ID)
	}
}
