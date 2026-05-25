package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newApp_WithOpenConvCapture(t *testing.T) (*App, *capturedOpenConv) {
	t.Helper()
	app := NewApp()
	app.currentUserID = "USELF"
	app.SetUserNames(map[string]string{"USELF": "Me", "U1": "Alice", "U2": "Bob"})

	cap := &capturedOpenConv{}
	app.SetChannelService(NewChannelService(ChannelServiceFuncs{
		OpenConversation: func(userIDs []string, requestID uint64) tea.Cmd {
			cap.calls = append(cap.calls, openConvCall{UserIDs: userIDs, RequestID: requestID})
			return nil // tests synthesize the result message directly
		},
	}))
	return app, cap
}

type openConvCall struct {
	UserIDs   []string
	RequestID uint64
}

type capturedOpenConv struct {
	calls []openConvCall
}

func TestReducer_EnterNewMessageMsg_OpensPickerAndEntersMode(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)

	_, _ = app.Update(EnterNewMessageMsg{})

	if app.mode != ModeNewMessage {
		t.Errorf("expected ModeNewMessage, got %v", app.mode)
	}
	if !app.newMessagePicker.IsVisible() {
		t.Error("expected picker visible")
	}
}

func TestReducer_NewMessageOpenedMsg_SwitchesChannelAndEntersInsertMode(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 7 // simulate an in-flight submit

	_, cmd := app.Update(NewMessageOpenedMsg{
		ChannelID:   "D123",
		AlreadyOpen: true,
		UserIDs:     []string{"U1"},
		RequestID:   7,
	})

	if app.mode != ModeInsert {
		t.Errorf("expected ModeInsert after opened msg, got %v", app.mode)
	}
	if app.newMessagePicker.IsVisible() {
		t.Error("expected picker closed")
	}
	// The reducer should emit a ChannelSelectedMsg.
	if cmd == nil {
		t.Fatal("expected a tea.Cmd that fires ChannelSelectedMsg")
	}
	msg := cmd()
	sel, ok := msg.(ChannelSelectedMsg)
	if !ok {
		t.Fatalf("expected ChannelSelectedMsg, got %T", msg)
	}
	if sel.ID != "D123" {
		t.Errorf("expected ChannelID=D123, got %s", sel.ID)
	}
}

func TestReducer_NewMessageOpenedMsg_DroppedWhenCancelled(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 7
	app.newMessageCancelled = true
	priorMode := app.mode

	_, cmd := app.Update(NewMessageOpenedMsg{
		ChannelID: "D123",
		RequestID: 7,
	})

	if app.mode != priorMode {
		t.Errorf("mode should not change for cancelled result, got %v", app.mode)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for cancelled result, got %T", cmd())
	}
}

func TestReducer_NewMessageOpenedMsg_DroppedWhenRequestIDMismatches(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 9 // current in-flight is 9
	priorMode := app.mode

	// Late arrival for an older request 7.
	_, cmd := app.Update(NewMessageOpenedMsg{
		ChannelID: "D123",
		RequestID: 7,
	})

	if app.mode != priorMode {
		t.Errorf("mode should not change for stale RequestID, got %v", app.mode)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for stale result")
	}
}

func TestReducer_NewMessageOpenedMsg_AlreadyOpenSkipsCacheInsert(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 1
	priorCount := len(app.sidebar.AllItems())

	_, _ = app.Update(NewMessageOpenedMsg{
		ChannelID:   "D456",
		AlreadyOpen: true,
		UserIDs:     []string{"U1"},
		RequestID:   1,
	})

	if got := len(app.sidebar.AllItems()); got != priorCount {
		t.Errorf("expected no sidebar mutation for AlreadyOpen=true, count went from %d to %d", priorCount, got)
	}
}

func TestReducer_NewMessageOpenedMsg_NewChannelInsertedAsDM(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 1

	_, _ = app.Update(NewMessageOpenedMsg{
		ChannelID:   "D789",
		AlreadyOpen: false,
		UserIDs:     []string{"U1"},
		RequestID:   1,
	})

	found := false
	for _, ch := range app.sidebar.AllItems() {
		if ch.ID == "D789" {
			found = true
			if ch.Type != "dm" {
				t.Errorf("expected Type=dm, got %s", ch.Type)
			}
			if ch.DMUserID != "U1" {
				t.Errorf("expected DMUserID=U1, got %s", ch.DMUserID)
			}
		}
	}
	if !found {
		t.Error("expected D789 inserted into sidebar")
	}
}

func TestReducer_NewMessageOpenedMsg_NewChannelInsertedAsGroupDM(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 1

	_, _ = app.Update(NewMessageOpenedMsg{
		ChannelID:   "G999",
		AlreadyOpen: false,
		UserIDs:     []string{"U1", "U2"},
		RequestID:   1,
	})

	found := false
	for _, ch := range app.sidebar.AllItems() {
		if ch.ID == "G999" {
			found = true
			if ch.Type != "group_dm" {
				t.Errorf("expected Type=group_dm, got %s", ch.Type)
			}
		}
	}
	if !found {
		t.Error("expected G999 inserted into sidebar")
	}
}

func TestReducer_NewMessageFailedMsg_KeepsModalOpenAndEmitsToast(t *testing.T) {
	app, _ := newApp_WithOpenConvCapture(t)
	_, _ = app.Update(EnterNewMessageMsg{})
	app.newMessageInFlightID = 7

	_, cmd := app.Update(NewMessageFailedMsg{
		RequestID: 7,
		Err:       errors.New("rate_limited"),
	})

	if app.mode != ModeNewMessage {
		t.Errorf("expected to stay in ModeNewMessage on failure, got %v", app.mode)
	}
	if !app.newMessagePicker.IsVisible() {
		t.Error("expected picker still visible on failure")
	}
	if cmd == nil {
		t.Fatal("expected a tea.Cmd emitting ToastMsg")
	}
	msg := cmd()
	toast, ok := msg.(ToastMsg)
	if !ok {
		t.Fatalf("expected ToastMsg, got %T", msg)
	}
	if toast.Text == "" {
		t.Error("expected non-empty toast text")
	}
}
