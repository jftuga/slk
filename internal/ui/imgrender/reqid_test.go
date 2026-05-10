package imgrender

import "testing"

func TestImageReadyMsg_HasReqID(t *testing.T) {
	m := ImageReadyMsg{Key: "k", ReqID: 42}
	if m.ReqID != 42 {
		t.Fatalf("got %d", m.ReqID)
	}
}

func TestImageFailedMsg_HasReqID(t *testing.T) {
	m := ImageFailedMsg{Key: "k", ReqID: 42}
	if m.ReqID != 42 {
		t.Fatalf("got %d", m.ReqID)
	}
}
