package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/styles"
)

// buildMsgViewForTest reproduces the inner-content render the messages-top
// path feeds to the border (ViewBare + ReapplyBgAfterResets), plus the
// derived geometry, for the current selection/focus.
func buildMsgViewForTest(a *App, focused bool) (msgView string, msgWidth, msgBorder, topHeight, msgContentHeight int) {
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	msgWidth = frame.MsgWidth
	msgBorder = frame.MsgBorder
	msgContentHeight = frame.ContentHeight - 2 - 3
	if msgContentHeight < 3 {
		msgContentHeight = 3
	}
	topHeight = msgContentHeight + 1
	a.messagepane.SetFocused(focused)
	msgView = messages.ReapplyBgAfterResets(a.messagepane.ViewBare(msgContentHeight, msgWidth-2), messages.BgANSI())
	return
}

func topBorderStyleForTest(msgWidth int, focused bool) lipgloss.Style {
	bs := styles.UnfocusedBorder
	if focused {
		bs = styles.FocusedBorder
	}
	return bs.Width(msgWidth).
		BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
}

// TestBorderedTopPane_MatchesLipgloss is the correctness gate for the
// zero-measurement border assembly. For many (focus, scroll-position,
// content) states it asserts borderedTopPane produces output that is
// visually identical to the lipgloss border + padPaneToSize path: same
// row count, every row exactly fullWidth display cells, and identical
// plain text per row. This also enforces the innerWidth invariant -- a
// short/unpadded source line makes a row narrower than fullWidth and
// fails here.
func TestBorderedTopPane_MatchesLipgloss(t *testing.T) {
	cases := []struct {
		name string
		app  func() *App
	}{
		{"wide-200msgs", makeWideScrollApp},
		{"with-topic", func() *App {
			a := makeWideScrollApp()
			a.messagepane.SetChannel("channel-1", "a moderately long channel topic that should wrap or fill the header line")
			return a
		}},
		{"few-msgs-no-overflow", func() *App {
			a := NewApp()
			_, _ = a.Update(tea.WindowSizeMsg{Width: 477, Height: 130})
			msgs := make([]messages.MessageItem, 3)
			for i := range msgs {
				msgs[i] = messages.MessageItem{TS: fmt.Sprintf("%d.0", 1700000000+i), UserName: "alice", UserID: "U1", Text: "short", Timestamp: "10:30 AM"}
			}
			a.messagepane.SetMessages(msgs)
			a.focusedPanel = PanelMessages
			a.SetMode(ModeNormal)
			_ = a.View()
			return a
		}},
	}

	for _, tc := range cases {
		for _, focused := range []bool{true, false} {
			for _, sel := range []int{-1, 0, 1} { // bottom, top, +1 from top
				a := tc.app()
				switch sel {
				case 0:
					a.messagepane.GoToTop()
				case 1:
					a.messagepane.GoToTop()
					a.messagepane.MoveDown()
				}

				msgView, msgWidth, msgBorder, topHeight, _ := buildMsgViewForTest(a, focused)
				full := msgWidth + msgBorder

				want := padPaneToSize(topBorderStyleForTest(msgWidth, focused).Render(msgView), msgWidth, full, topHeight, styles.Background)
				got := borderedTopPane(msgView, msgWidth-2, full, topHeight, focused, styles.Background)

				wl := strings.Split(want, "\n")
				gl := strings.Split(got, "\n")
				if len(wl) != topHeight || len(gl) != topHeight {
					t.Fatalf("[%s focused=%v sel=%d] rows: want=%d got=%d (topHeight=%d)", tc.name, focused, sel, len(wl), len(gl), topHeight)
				}
				for i := range wl {
					gw := ansi.StringWidth(gl[i])
					if gw != full {
						t.Fatalf("[%s focused=%v sel=%d] row %d width=%d want=%d (short source line?)\n got=%q", tc.name, focused, sel, i, gw, full, gl[i])
					}
					if ansi.Strip(wl[i]) != ansi.Strip(gl[i]) {
						t.Fatalf("[%s focused=%v sel=%d] row %d plaintext differs:\n want=%q\n got =%q", tc.name, focused, sel, i, ansi.Strip(wl[i]), ansi.Strip(gl[i]))
					}
				}
			}
		}
	}
}
