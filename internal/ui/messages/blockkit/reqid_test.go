package blockkit

import "testing"

func TestBlockImageReadyMsg_HasReqID(t *testing.T) {
	m := BlockImageReadyMsg{URL: "u", ReqID: 42}
	if m.ReqID != 42 {
		t.Fatalf("got %d", m.ReqID)
	}
}
