package cache

import (
	"testing"
)

func TestChannelsWithMessages_EmptyWorkspace(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()

	got, err := db.ChannelsWithMessages("T1")
	if err != nil {
		t.Fatalf("ChannelsWithMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d rows: %+v", len(got), got)
	}
}

func TestChannelsWithMessages_ReturnsChannelsWithAnyMessage(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()

	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T1", Name: "random", Type: "channel"})
	db.UpsertChannel(Channel{ID: "C3", WorkspaceID: "T1", Name: "empty", Type: "channel"})
	db.SetChannelSyncedAt("C1", 1700000000)
	db.SetChannelSyncedAt("C2", 1700001000)

	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U1", Text: "hi"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U1", Text: "yo"})

	got, err := db.ChannelsWithMessages("T1")
	if err != nil {
		t.Fatalf("ChannelsWithMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(got), got)
	}
	byID := map[string]ChannelSyncRow{}
	for _, r := range got {
		byID[r.ChannelID] = r
	}
	if byID["C1"].SyncedAt != 1700000000 {
		t.Errorf("C1 synced_at = %d, want 1700000000", byID["C1"].SyncedAt)
	}
	if byID["C2"].SyncedAt != 1700001000 {
		t.Errorf("C2 synced_at = %d, want 1700001000", byID["C2"].SyncedAt)
	}
	if _, present := byID["C3"]; present {
		t.Errorf("C3 (no messages) should not be in result")
	}
}

func TestChannelsWithMessages_ChannelRowMissing(t *testing.T) {
	// A message can land via WS for a channel never UpsertChannel'd
	// (the OnMessage handler only upserts the message, not the channel).
	// In that case synced_at is 0.
	db := setupDBWithWorkspace(t)
	defer db.Close()

	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C99", WorkspaceID: "T1", UserID: "U1", Text: "orphan"})

	got, err := db.ChannelsWithMessages("T1")
	if err != nil {
		t.Fatalf("ChannelsWithMessages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].ChannelID != "C99" || got[0].SyncedAt != 0 {
		t.Errorf("got %+v, want {C99, 0}", got[0])
	}
}

func TestChannelsWithMessages_WorkspaceIsolation(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertWorkspace(Workspace{ID: "T2", Name: "Other"})

	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T2", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U1", Text: "a"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C2", WorkspaceID: "T2", UserID: "U1", Text: "b"})

	got, err := db.ChannelsWithMessages("T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChannelID != "C1" {
		t.Errorf("expected only C1, got %+v", got)
	}
}
