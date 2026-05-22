package mentionpicker

import (
	"reflect"
	"strings"
	"testing"
)

func TestFilterByDisplayNamePrefix(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice", Username: "alice"},
		{ID: "U2", DisplayName: "Bob", Username: "bob"},
		{ID: "U3", DisplayName: "Alicia", Username: "alicia.j"},
	})
	m.Open()
	m.SetQuery("ali")
	if len(m.Filtered()) != 2 {
		t.Fatalf("expected 2 filtered users, got %d", len(m.Filtered()))
	}
	if m.Filtered()[0].ID != "U1" {
		t.Errorf("expected Alice first, got %s", m.Filtered()[0].DisplayName)
	}
}

func TestFilterByUsernamePrefix(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice Smith", Username: "asmith"},
		{ID: "U2", DisplayName: "Bob Jones", Username: "bjones"},
	})
	m.Open()
	m.SetQuery("asm")
	if len(m.Filtered()) != 1 {
		t.Fatalf("expected 1 filtered user, got %d", len(m.Filtered()))
	}
	if m.Filtered()[0].ID != "U1" {
		t.Errorf("expected Alice Smith, got %s", m.Filtered()[0].DisplayName)
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice", Username: "alice"},
	})
	m.Open()
	m.SetQuery("ALI")
	if len(m.Filtered()) != 1 {
		t.Fatalf("expected 1 filtered user, got %d", len(m.Filtered()))
	}
}

func TestFilterEmptyQueryShowsAll(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice", Username: "alice"},
		{ID: "U2", DisplayName: "Bob", Username: "bob"},
		{ID: "U3", DisplayName: "Carol", Username: "carol"},
		{ID: "U4", DisplayName: "Dave", Username: "dave"},
		{ID: "U5", DisplayName: "Eve", Username: "eve"},
		{ID: "U6", DisplayName: "Frank", Username: "frank"},
	})
	m.Open()
	m.SetQuery("")
	// Empty query shows specials (3) + first users up to MaxVisible=7 total
	if len(m.Filtered()) != 7 {
		t.Fatalf("expected 7 filtered users (max), got %d", len(m.Filtered()))
	}
}

func TestFilterSpecialMentions(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Henry", Username: "henry"},
	})
	m.Open()
	m.SetQuery("he")
	filtered := m.Filtered()
	foundHere := false
	for _, u := range filtered {
		if u.ID == "special:here" {
			foundHere = true
		}
	}
	if !foundHere {
		t.Error("expected @here in filtered results")
	}
}

func TestOpenClose(t *testing.T) {
	m := New()
	if m.IsVisible() {
		t.Error("expected not visible initially")
	}
	m.Open()
	if !m.IsVisible() {
		t.Error("expected visible after Open")
	}
	m.Close()
	if m.IsVisible() {
		t.Error("expected not visible after Close")
	}
}

func TestMoveUpDown(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice", Username: "alice"},
		{ID: "U2", DisplayName: "Bob", Username: "bob"},
	})
	m.Open()
	m.SetQuery("")
	if m.Selected() != 0 {
		t.Errorf("expected selected=0, got %d", m.Selected())
	}
	m.MoveDown()
	if m.Selected() != 1 {
		t.Errorf("expected selected=1, got %d", m.Selected())
	}
	m.MoveUp()
	if m.Selected() != 0 {
		t.Errorf("expected selected=0, got %d", m.Selected())
	}
	m.MoveUp()
	if m.Selected() != 0 {
		t.Errorf("expected selected=0 (clamped), got %d", m.Selected())
	}
}

func TestSelectReturnsResult(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "Alice", Username: "alice"},
	})
	m.Open()
	m.SetQuery("alice")
	result := m.Select()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.UserID != "U1" {
		t.Errorf("expected U1, got %s", result.UserID)
	}
	if result.DisplayName != "Alice" {
		t.Errorf("expected Alice, got %s", result.DisplayName)
	}
}

func TestSelectEmptyReturnsNil(t *testing.T) {
	m := New()
	m.SetUsers([]User{})
	m.Open()
	m.SetQuery("zzz")
	result := m.Select()
	if result != nil {
		t.Error("expected nil result for empty filtered list")
	}
}

func TestFilterSortsInChannelBeforeNotInChannel(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "alice", InChannel: false},
		{ID: "U2", DisplayName: "bob", InChannel: true},
		{ID: "U3", DisplayName: "charlie", InChannel: false},
		{ID: "U4", DisplayName: "dan", InChannel: true},
	})
	m.Open()
	got := m.Filtered()
	// Special mentions match empty query too. Strip them to assert user order.
	var names []string
	for _, u := range got {
		if u.ID == "U1" || u.ID == "U2" || u.ID == "U3" || u.ID == "U4" {
			names = append(names, u.DisplayName)
		}
	}
	want := []string{"bob", "dan", "alice", "charlie"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("user order = %v, want %v", names, want)
	}
}

func TestFilterAlphabeticalWithinTier(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "zoe", InChannel: true},
		{ID: "U2", DisplayName: "alex", InChannel: true},
		{ID: "U3", DisplayName: "mia", InChannel: true},
	})
	m.Open()
	var names []string
	for _, u := range m.Filtered() {
		if u.ID != "" && u.ID[:1] == "U" {
			names = append(names, u.DisplayName)
		}
	}
	want := []string{"alex", "mia", "zoe"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("alpha order = %v, want %v", names, want)
	}
}

func TestSpecialMentionsAlwaysInChannel(t *testing.T) {
	m := New()
	m.SetUsers(nil)
	m.Open()
	for _, u := range m.Filtered() {
		if !u.InChannel {
			t.Errorf("special mention %s should have InChannel=true", u.DisplayName)
		}
	}
}

func TestMaxVisibleSeven(t *testing.T) {
	if MaxVisible != 7 {
		t.Errorf("MaxVisible = %d, want 7", MaxVisible)
	}
}

func TestViewNoEmptyParensForRegularUser(t *testing.T) {
	m := New()
	m.SetUsers([]User{{ID: "U1", DisplayName: "jane.doe", InChannel: true}})
	m.Open()
	out := m.View(40)
	if strings.Contains(out, "()") {
		t.Errorf("view contains empty parens: %q", out)
	}
	if !strings.Contains(out, "jane.doe") {
		t.Errorf("view missing display name: %q", out)
	}
}

func TestViewKeepsParensForSpecialMentions(t *testing.T) {
	m := New()
	m.SetUsers(nil)
	m.Open()
	out := m.View(40)
	for _, want := range []string{"(here)", "(channel)", "(everyone)"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q: %q", want, out)
		}
	}
}

func TestViewExternalSuffix(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "jenny.kim", InChannel: true, IsExternal: true},
	})
	m.Open()
	out := m.View(60)
	if !strings.Contains(out, "(ext)") {
		t.Errorf("expected (ext) suffix: %q", out)
	}
}

func TestViewNotInChannelSuffix(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "jordan.lee", InChannel: false},
	})
	m.Open()
	out := m.View(60)
	if !strings.Contains(out, "(not in channel)") {
		t.Errorf("expected (not in channel) suffix: %q", out)
	}
}

func TestFilterAccentInsensitive(t *testing.T) {
	m := New()
	m.SetUsers([]User{
		{ID: "U1", DisplayName: "François", Username: "françois.b"},
		{ID: "U2", DisplayName: "Mélanie", Username: "mélanie"},
		{ID: "U3", DisplayName: "Alice", Username: "alice"},
	})
	cases := []struct {
		query  string
		wantID string
	}{
		{"francois", "U1"},
		{"melanie", "U2"},
		{"François", "U1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.query, func(t *testing.T) {
			m.Open()
			m.SetQuery(tc.query)
			if len(m.Filtered()) == 0 {
				t.Fatalf("expected at least 1 match for %q", tc.query)
			}
			if m.Filtered()[0].ID != tc.wantID {
				t.Errorf("query %q: expected %s, got %s",
					tc.query, tc.wantID, m.Filtered()[0].ID)
			}
		})
	}
}
