package cache

import (
	"sort"
	"testing"
)

// seedThreadFixtures inserts a workspace, a few channels, and several
// thread parents + replies for testing ListInvolvedThreads.
func seedThreadFixtures(t *testing.T, db *DB, selfID string) {
	t.Helper()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true, LastReadTS: "1700000000.000000"})
	db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T1", Name: "design", Type: "channel", IsMember: true, LastReadTS: "1700000500.000000"})

	// Thread A in C1: self authored parent, others replied. Unread (last reply > last_read, by other).
	db.UpsertMessage(Message{TS: "1700000100.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: selfID, Text: "started by me", ThreadTS: "1700000100.000000"})
	db.UpsertMessage(Message{TS: "1700000200.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "reply by other", ThreadTS: "1700000100.000000"})

	// Thread B in C2: someone else's parent, self replied. Read (last reply by self).
	db.UpsertMessage(Message{TS: "1700000300.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U2", Text: "alice parent", ThreadTS: "1700000300.000000"})
	db.UpsertMessage(Message{TS: "1700000400.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: selfID, Text: "my reply", ThreadTS: "1700000300.000000"})

	// Thread C in C1: self mentioned in parent, no reply by self. Unread.
	db.UpsertMessage(Message{TS: "1700000600.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U3", Text: "hey <@" + selfID + "> ping", ThreadTS: "1700000600.000000"})
	db.UpsertMessage(Message{TS: "1700000700.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U3", Text: "follow up", ThreadTS: "1700000600.000000"})

	// Thread D in C2: not involved (no self, no mention). Should be excluded.
	db.UpsertMessage(Message{TS: "1700000800.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U4", Text: "unrelated", ThreadTS: "1700000800.000000"})
	db.UpsertMessage(Message{TS: "1700000900.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U5", Text: "also unrelated", ThreadTS: "1700000800.000000"})
}

func TestListInvolvedThreads_IncludesAuthoredRepliedMentioned(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	seedThreadFixtures(t, db, "USELF")

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatalf("ListInvolvedThreads: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 involved threads, got %d: %+v", len(got), got)
	}
	threadTSs := []string{}
	for _, s := range got {
		threadTSs = append(threadTSs, s.ThreadTS)
	}
	sort.Strings(threadTSs)
	want := []string{"1700000100.000000", "1700000300.000000", "1700000600.000000"}
	for i := range want {
		if threadTSs[i] != want[i] {
			t.Errorf("threadTSs[%d] = %s, want %s", i, threadTSs[i], want[i])
		}
	}
}

func TestListInvolvedThreads_OrderingByLastReplyTS(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	seedThreadFixtures(t, db, "USELF")

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 threads, got %d", len(got))
	}
	// Sort is now purely LastReplyTS DESC.
	// Thread C last_reply_ts = 700, Thread B last_reply_ts = 400, Thread A last_reply_ts = 200.
	if got[0].ThreadTS != "1700000600.000000" {
		t.Errorf("got[0] = %s, want C (1700000600.000000)", got[0].ThreadTS)
	}
	if got[1].ThreadTS != "1700000300.000000" {
		t.Errorf("got[1] = %s, want B (1700000300.000000)", got[1].ThreadTS)
	}
	if got[2].ThreadTS != "1700000100.000000" {
		t.Errorf("got[2] = %s, want A (1700000100.000000)", got[2].ThreadTS)
	}
}

func TestListInvolvedThreads_UnreadDoesNotChangeOrder(t *testing.T) {
	// Regression for the screenshot bug: an Unread=true thread with
	// an older LastReplyTS must NOT sort ahead of an Unread=false
	// thread with a newer LastReplyTS.
	db := setupDBWithWorkspace(t)
	defer db.Close()
	// Channel with empty last_read_ts → Unread heuristic at threads.go:95
	// flips to true whenever LastReplyBy != selfUserID.
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})

	// Older thread: someone-else replied last → Unread=true under heuristic.
	db.UpsertMessage(Message{TS: "1000.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "old self parent", ThreadTS: "1000.000000"})
	db.UpsertMessage(Message{TS: "1100.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "old other reply", ThreadTS: "1000.000000"})

	// Newer thread: self replied last → Unread=false.
	db.UpsertMessage(Message{TS: "2000.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "newer parent", ThreadTS: "2000.000000"})
	db.UpsertMessage(Message{TS: "2100.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "newer self reply", ThreadTS: "2000.000000"})

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(got))
	}
	if got[0].ThreadTS != "2000.000000" {
		t.Errorf("got[0] = %s, want newer thread 2000.000000 (LastReplyTS DESC must win regardless of Unread)", got[0].ThreadTS)
	}
	if got[1].ThreadTS != "1000.000000" {
		t.Errorf("got[1] = %s, want older thread 1000.000000", got[1].ThreadTS)
	}
	// And confirm Unread heuristic still computes as expected — the
	// dot indicator should still light up.
	if !got[1].Unread {
		t.Errorf("got[1] (older thread with other-replied-last) should still be Unread=true under heuristic")
	}
	if got[0].Unread {
		t.Errorf("got[0] (newer thread with self-replied-last) should be Unread=false")
	}
}

func TestListInvolvedThreads_PopulatesParentAndReplyCount(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	seedThreadFixtures(t, db, "USELF")

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	byTS := map[string]ThreadSummary{}
	for _, s := range got {
		byTS[s.ThreadTS] = s
	}

	a := byTS["1700000100.000000"]
	if a.ParentUserID != "USELF" || a.ParentText != "started by me" {
		t.Errorf("thread A parent wrong: %+v", a)
	}
	if a.ReplyCount != 1 {
		t.Errorf("thread A reply count = %d, want 1", a.ReplyCount)
	}
	if a.LastReplyBy != "U2" {
		t.Errorf("thread A last reply by = %s, want U2", a.LastReplyBy)
	}
	if a.ChannelName != "general" || a.ChannelType != "channel" {
		t.Errorf("thread A channel wrong: %+v", a)
	}
}

func TestListInvolvedThreads_MentionRequiresAngleBrackets(t *testing.T) {
	// Plain "USELF" in text without <@…> wrapping must NOT count as a mention.
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "the user USELF mentioned in plain text", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "more", ThreadTS: "1.000000"})

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 threads, got %d", len(got))
	}
}

func TestListInvolvedThreads_ParentMissingFromCache(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true})
	// Reply by self exists; parent does not.
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "my reply", ThreadTS: "1.000000"})

	got, err := db.ListInvolvedThreads("T1", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(got))
	}
	if got[0].ParentUserID != "" || got[0].ParentText != "" {
		t.Errorf("missing parent should leave ParentUserID/ParentText empty, got %+v", got[0])
	}
	if got[0].ThreadTS != "1.000000" {
		t.Errorf("ThreadTS = %s, want 1.000000", got[0].ThreadTS)
	}
}

func TestListInvolvedThreads_PerWorkspaceIsolation(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertWorkspace(Workspace{ID: "T2", Name: "Other"})
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true})
	db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T2", Name: "general", Type: "channel", IsMember: true})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "T1 thread", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "reply", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "3.000000", ChannelID: "C2", WorkspaceID: "T2", UserID: "USELF", Text: "T2 thread", ThreadTS: "3.000000"})
	db.UpsertMessage(Message{TS: "4.000000", ChannelID: "C2", WorkspaceID: "T2", UserID: "U2", Text: "reply", ThreadTS: "3.000000"})

	got1, _ := db.ListInvolvedThreads("T1", "USELF")
	got2, _ := db.ListInvolvedThreads("T2", "USELF")
	if len(got1) != 1 || got1[0].ThreadTS != "1.000000" {
		t.Errorf("T1 query should return only T1 thread, got %+v", got1)
	}
	if len(got2) != 1 || got2[0].ThreadTS != "3.000000" {
		t.Errorf("T2 query should return only T2 thread, got %+v", got2)
	}
}

func TestThreadInvolvesUser_AuthoredParent(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "parent", ThreadTS: "1.000000"})

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if !involved {
		t.Error("self-authored parent should count as involved")
	}
}

func TestThreadInvolvesUser_RepliedToThread(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "parent", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "my reply", ThreadTS: "1.000000"})

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if !involved {
		t.Error("self reply should count as involved")
	}
}

func TestThreadInvolvesUser_MentionedAngleBracket(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "hey <@USELF> ping", ThreadTS: "1.000000"})

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if !involved {
		t.Error("<@USELF> mention should count as involved")
	}
}

func TestThreadInvolvesUser_PlainTextNotInvolved(t *testing.T) {
	// Bare "USELF" without <@…> wrapping must NOT count, matching
	// ListInvolvedThreads' semantics.
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "discussing USELF in plain text", ThreadTS: "1.000000"})

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if involved {
		t.Error("plain-text USELF should not count as involved")
	}
}

func TestThreadInvolvesUser_NoneMatch(t *testing.T) {
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "parent", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U3", Text: "reply", ThreadTS: "1.000000"})

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if involved {
		t.Error("no self / no mention thread should not count")
	}
}

func TestThreadInvolvesUser_RespectsDeleted(t *testing.T) {
	// A deleted message should not count as involvement, matching the
	// is_deleted = 0 clause in ListInvolvedThreads.
	db := setupDBWithWorkspace(t)
	defer db.Close()
	db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"})
	db.UpsertMessage(Message{TS: "1.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U2", Text: "parent", ThreadTS: "1.000000"})
	db.UpsertMessage(Message{TS: "2.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "USELF", Text: "my reply", ThreadTS: "1.000000"})
	if err := db.DeleteMessage("C1", "2.000000"); err != nil {
		t.Fatal(err)
	}

	involved, err := db.ThreadInvolvesUser("T1", "C1", "1.000000", "USELF")
	if err != nil {
		t.Fatal(err)
	}
	if involved {
		t.Error("deleted self reply should not count as involved")
	}
}

// --- ListSubscribedThreads tests ---

// seedSubscribedThreadFixtures wires up two subscribed threads: A in
// channel C1 (unread — last_reply > LastRead, last reply by other),
// and B in channel C2 (read — last_reply == LastRead).
// Plus one unsubscribed-but-still-cached thread D in C1 that must
// NOT appear in the result.
func seedSubscribedThreadFixtures(t *testing.T, db *DB, selfID string) {
	t.Helper()
	if err := db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true}); err != nil {
		t.Fatalf("UpsertChannel C1: %v", err)
	}
	if err := db.UpsertChannel(Channel{ID: "C2", WorkspaceID: "T1", Name: "design", Type: "channel", IsMember: true}); err != nil {
		t.Fatalf("UpsertChannel C2: %v", err)
	}

	// Thread A in C1: parent by another user, one reply by another.
	// Subscribed; unread because last reply > LastRead and last reply by other.
	mustUpsertMsg(t, db, "1700000100.000000", "C1", "U2", "parent A", "1700000100.000000")
	mustUpsertMsg(t, db, "1700000200.000000", "C1", "U3", "reply A1", "1700000100.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription A: %v", err)
	}

	// Thread B in C2: parent by self, one reply by other.
	// Subscribed; read because LastRead == last reply.
	mustUpsertMsg(t, db, "1700000300.000000", "C2", selfID, "parent B", "1700000300.000000")
	mustUpsertMsg(t, db, "1700000400.000000", "C2", "U2", "reply B1", "1700000300.000000")
	if err := db.UpsertThreadSubscription("T1", "C2", "1700000300.000000", "1700000400.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription B: %v", err)
	}

	// Thread D in C1: parent + reply cached, but UNSUBSCRIBED.
	mustUpsertMsg(t, db, "1700000500.000000", "C1", "U2", "parent D", "1700000500.000000")
	mustUpsertMsg(t, db, "1700000600.000000", "C1", "U3", "reply D1", "1700000500.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000500.000000", "1700000500.000000", false); err != nil {
		t.Fatalf("UpsertThreadSubscription D (tombstone): %v", err)
	}
}

func mustUpsertMsg(t *testing.T, db *DB, ts, channelID, userID, text, threadTS string) {
	t.Helper()
	if err := db.UpsertMessage(Message{
		TS: ts, ChannelID: channelID, WorkspaceID: "T1", UserID: userID, Text: text, ThreadTS: threadTS,
	}); err != nil {
		t.Fatalf("UpsertMessage %s: %v", ts, err)
	}
}

func TestListSubscribedThreads_OnlySubscribedShows(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	seedSubscribedThreadFixtures(t, db, selfID)

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 subscribed threads (A and B), got %d: %+v", len(got), got)
	}
	keys := map[string]bool{}
	for _, s := range got {
		keys[s.ChannelID+":"+s.ThreadTS] = true
	}
	if !keys["C1:1700000100.000000"] || !keys["C2:1700000300.000000"] {
		t.Fatalf("missing expected threads, got keys: %v", keys)
	}
	if keys["C1:1700000500.000000"] {
		t.Fatalf("unsubscribed thread D leaked into result")
	}
}

func TestListSubscribedThreads_SortByLastReplyTSDesc(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	seedSubscribedThreadFixtures(t, db, selfID)

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want >=2, got %d", len(got))
	}
	// B has last_reply 1700000400 > A's 1700000200, so B sorts first.
	if got[0].ChannelID != "C2" {
		t.Fatalf("expected B (C2) first, got %s", got[0].ChannelID)
	}
}

func TestListSubscribedThreads_UnreadUsesPerThreadLastRead(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	// Set the channel's last_read_ts to a value AFTER the last reply —
	// the old heuristic would say "read", but the per-thread LastRead
	// from thread_subscriptions says "unread".
	if err := db.UpsertChannel(Channel{
		ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel", IsMember: true,
		LastReadTS: "1700000999.000000",
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	mustUpsertMsg(t, db, "1700000100.000000", "C1", "U2", "parent", "1700000100.000000")
	mustUpsertMsg(t, db, "1700000200.000000", "C1", "U3", "reply", "1700000100.000000")
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if !got[0].Unread {
		t.Fatalf("expected Unread=true (per-thread LastRead=...150 < LastReplyTS=...200), got Unread=false")
	}
}

func TestListSubscribedThreads_ParentMissingShowsEmpty(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	// Subscription exists, but neither parent nor replies are cached.
	if err := db.UpsertChannel(Channel{ID: "C1", WorkspaceID: "T1", Name: "general", Type: "channel"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].ParentText != "" || got[0].ParentUserID != "" {
		t.Fatalf("expected empty parent fields for uncached thread, got %+v", got[0])
	}
	// LastReplyTS falls back to the subscription's LastRead when no
	// messages are cached for the thread.
	if got[0].LastReplyTS != "1700000150.000000" {
		t.Fatalf("expected LastReplyTS to fall back to subscription LastRead, got %q", got[0].LastReplyTS)
	}
}

func TestListSubscribedThreads_PerWorkspaceIsolation(t *testing.T) {
	const selfID = "U1"
	db := setupDBWithWorkspace(t)
	if err := db.UpsertWorkspace(Workspace{ID: "T2", Name: "T2"}); err != nil {
		t.Fatalf("UpsertWorkspace T2: %v", err)
	}
	if err := db.UpsertChannel(Channel{ID: "C9", WorkspaceID: "T2", Name: "other"}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	if err := db.UpsertThreadSubscription("T2", "C9", "1700000100.000000", "1700000150.000000", true); err != nil {
		t.Fatalf("UpsertThreadSubscription T2: %v", err)
	}

	got, err := db.ListSubscribedThreads("T1", selfID)
	if err != nil {
		t.Fatalf("ListSubscribedThreads: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("T1 should have 0 subscribed threads, got %d", len(got))
	}
}
