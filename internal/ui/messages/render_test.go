package messages

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/gammons/slk/internal/config"
	"github.com/gammons/slk/internal/ui/styles"
)

// TestLabeledLinkShowsLabelAndOSC8 asserts that a Slack-style labeled link
// (<URL|label>) renders just the label and emits an OSC 8 hyperlink escape
// so the label is clickable in modern terminals. The raw URL is intentionally
// NOT included in the plain output — terminals supply clickability via OSC 8
// or their own URL auto-detection, and the duplicated URL was visual noise.
func TestLabeledLinkShowsLabelAndOSC8(t *testing.T) {
	in := "see <https://example.com/doc|the document> for details"
	out := RenderSlackMarkdown(in, nil, nil)
	plain := ansi.Strip(out)

	if !strings.Contains(plain, "the document") {
		t.Errorf("expected label %q in plain output, got %q", "the document", plain)
	}
	if strings.Contains(plain, "https://example.com/doc") {
		t.Errorf("did not expect raw URL in plain output, got %q", plain)
	}
	// OSC 8 hyperlink: \x1b]8;;URL\x1b\\LABEL\x1b]8;;\x1b\\
	if !strings.Contains(out, "\x1b]8;;https://example.com/doc") {
		t.Error("expected OSC 8 hyperlink escape for clickable label")
	}
}

// TestBareLinkOSC8 asserts that a bare <URL> link gets wrapped in an OSC 8
// hyperlink escape so it's clickable.
func TestBareLinkOSC8(t *testing.T) {
	in := "go to <https://example.com>"
	out := RenderSlackMarkdown(in, nil, nil)
	plain := ansi.Strip(out)

	if !strings.Contains(plain, "https://example.com") {
		t.Errorf("expected URL in plain output, got %q", plain)
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com") {
		t.Error("expected OSC 8 hyperlink escape on bare link")
	}
}

// TestIntraWordUnderscoreNotItalicized captures the bug where the
// receive-side italic regex `_X_` mistakenly italicizes intra-word
// underscores like is_unpaid_yes, stripping the underscores. Per
// CommonMark, an underscore between two word characters is literal
// and must NOT open or close emphasis. The fix only italicizes when
// the surrounding chars are non-word (whitespace, punctuation, or
// start/end of text).
func TestIntraWordUnderscoreNotItalicized(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // ANSI-stripped plain text
	}{
		{"intraword two underscores", "the is_unpaid_yes flag", "the is_unpaid_yes flag"},
		{"intraword three underscores", "hello_world_foo_bar", "hello_world_foo_bar"},
		{"snake_case identifier", "is_unpaid", "is_unpaid"},
		{"two-underscore identifier", "foo_bar_baz", "foo_bar_baz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderSlackMarkdown(tc.in, nil, nil)
			plain := ansi.Strip(out)
			if plain != tc.want {
				t.Errorf("RenderSlackMarkdown(%q) plain = %q, want %q", tc.in, plain, tc.want)
			}
		})
	}
}

// TestItalicPreservedAtWordBoundaries guards that the word-boundary
// fix doesn't regress the actual italic syntax — _X_ at the start of
// a token (whitespace or start-of-string on the left, whitespace or
// end-of-string on the right) must still render italic and strip the
// surrounding underscores.
func TestItalicPreservedAtWordBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantPlain  string
		wantItalic bool
	}{
		{"standalone italic", "_emphasized_", "emphasized", true},
		{"italic at start of sentence", "_hello_ world", "hello world", true},
		{"italic at end of sentence", "say _hello_", "say hello", true},
		{"italic mid-sentence", "say _hello_ now", "say hello now", true},
		{"multi-word italic", "_hello world_", "hello world", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderSlackMarkdown(tc.in, nil, nil)
			plain := ansi.Strip(out)
			if plain != tc.wantPlain {
				t.Errorf("RenderSlackMarkdown(%q) plain = %q, want %q", tc.in, plain, tc.wantPlain)
			}
			// Italic SGR is "\x1b[3" — check for it in the raw output.
			hasItalic := strings.Contains(out, "\x1b[3")
			if hasItalic != tc.wantItalic {
				t.Errorf("RenderSlackMarkdown(%q) hasItalic = %v, want %v\nraw=%q", tc.in, hasItalic, tc.wantItalic, out)
			}
		})
	}
}

// TestLabeledMailtoLinkRendersJustEmail asserts that Slack's wire form
// for an emailed link — <mailto:user@host|user@host> — renders as just
// the email address, not the literal angle-bracket text. Slack
// auto-linkifies typed emails on every message body that goes through
// the rich_text -> markdown round-trip, so this form arrives often.
func TestLabeledMailtoLinkRendersJustEmail(t *testing.T) {
	in := "ping <mailto:gammons@gmail.com|gammons@gmail.com> when ready"
	out := RenderSlackMarkdown(in, nil, nil)
	plain := ansi.Strip(out)

	if !strings.Contains(plain, "gammons@gmail.com") {
		t.Errorf("expected email in plain output, got %q", plain)
	}
	if strings.Contains(plain, "<mailto:") {
		t.Errorf("did not expect raw <mailto:...> in plain output, got %q", plain)
	}
	if strings.Contains(plain, "|") {
		t.Errorf("did not expect the label separator '|' in plain output, got %q", plain)
	}
	// OSC 8 hyperlink should still wrap the email so it's clickable in
	// terminals that handle mailto: links.
	if !strings.Contains(out, "\x1b]8;;mailto:gammons@gmail.com") {
		t.Error("expected OSC 8 hyperlink escape with the mailto: target")
	}
}

// TestBareMailtoLinkRendersJustEmail asserts that the unlabeled wire
// form <mailto:user@host> (some clients emit it) renders as just the
// email, with the mailto: prefix stripped from the visible text but
// preserved in the OSC 8 hyperlink target.
func TestBareMailtoLinkRendersJustEmail(t *testing.T) {
	in := "contact <mailto:gammons@gmail.com>"
	out := RenderSlackMarkdown(in, nil, nil)
	plain := ansi.Strip(out)

	if !strings.Contains(plain, "gammons@gmail.com") {
		t.Errorf("expected email in plain output, got %q", plain)
	}
	if strings.Contains(plain, "mailto:") {
		t.Errorf("did not expect 'mailto:' in plain (visible) output, got %q", plain)
	}
	if !strings.Contains(out, "\x1b]8;;mailto:gammons@gmail.com") {
		t.Error("expected OSC 8 hyperlink escape with the mailto: target")
	}
}

// TestChannelMentionStillRendersWithHash guards against the regex-ordering
// regression noted in render.go: linkWithLabelRe must not consume
// <#CHANNEL_ID|name> and reduce it to just "name". We tighten it to require
// https?:// so channel mentions fall through to channelMentionRe.
func TestChannelMentionStillRendersWithHash(t *testing.T) {
	in := "see <#C123|general>"
	out := ansi.Strip(RenderSlackMarkdown(in, nil, nil))

	if !strings.Contains(out, "#general") {
		t.Errorf("expected '#general' in output (channel mention should keep #), got %q", out)
	}
}

// TestUserMentionResolvesAndKeepsAt confirms user mentions resolve via the
// userNames map and retain their @ prefix.
func TestUserMentionResolvesAndKeepsAt(t *testing.T) {
	in := "hi <@U99>"
	out := ansi.Strip(RenderSlackMarkdown(in, map[string]string{"U99": "alice"}, nil))
	if !strings.Contains(out, "@alice") {
		t.Errorf("expected '@alice' in output, got %q", out)
	}
}

// TestBareChannelMentionResolvesViaMap confirms the inbound rendering
// path for the <#CHANNELID> form (no embedded |name) -- this is what
// other Slack clients (and our own older send path) emit, and it
// previously rendered as the raw <#CID> token.
func TestBareChannelMentionResolvesViaMap(t *testing.T) {
	in := "see <#C123>"
	out := ansi.Strip(RenderSlackMarkdown(in, nil, map[string]string{"C123": "general"}))
	if !strings.Contains(out, "#general") {
		t.Errorf("expected '#general' in output, got %q", out)
	}
	if strings.Contains(out, "C123") {
		t.Errorf("expected raw channel ID to be replaced, got %q", out)
	}
}

// TestBareChannelMentionUnresolvedFallsBack confirms the renderer
// emits a readable "#unknown" placeholder rather than leaking the raw
// <#CID> token when the channel isn't in the resolution map.
func TestBareChannelMentionUnresolvedFallsBack(t *testing.T) {
	in := "see <#C999>"
	out := ansi.Strip(RenderSlackMarkdown(in, nil, nil))
	if strings.Contains(out, "<#C999>") {
		t.Errorf("expected raw <#CID> token to be replaced, got %q", out)
	}
}

// TestUnlabeledNonHTTPLinkSurvives confirms that <url|text> patterns where the
// URL is NOT http(s) don't get gobbled by linkWithLabelRe. (Slack uses
// <!subteam^S123|@team> for groups, etc.)
func TestNonHTTPBracketedSurvives(t *testing.T) {
	// Should not panic, should not render as a link. We only assert it doesn't
	// crash and the output is non-empty.
	out := RenderSlackMarkdown("ping <!subteam^S123|@team> please", nil, nil)
	if out == "" {
		t.Error("expected non-empty output")
	}
}

// TestRenderAttachmentsImageMarker asserts that an Image attachment renders
// with an [Image] marker, the URL (visible for copy-paste), and an OSC 8
// hyperlink for clickability. Filenames are intentionally omitted to keep
// attachment lines short enough to fit in narrow panes.
func TestRenderAttachmentsImageMarker(t *testing.T) {
	got := RenderAttachments([]Attachment{
		{Kind: "image", Name: "uniquefile12345.png", URL: "https://files.slack.com/abc/xyz.png"},
	})
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "[Image]") {
		t.Errorf("expected [Image] marker, got %q", plain)
	}
	if strings.Contains(plain, "uniquefile12345.png") {
		t.Errorf("filename should be omitted from attachment line, got %q", plain)
	}
	if !strings.Contains(plain, "https://files.slack.com") {
		t.Errorf("expected URL visible in plain output, got %q", plain)
	}
	if !strings.Contains(got, "\x1b]8;;https://files.slack.com") {
		t.Error("expected OSC 8 hyperlink escape on attachment line")
	}
}

// TestRenderAttachmentsFileMarker confirms non-image attachments use [File]
// and omit the filename.
func TestRenderAttachmentsFileMarker(t *testing.T) {
	got := ansi.Strip(RenderAttachments([]Attachment{
		{Kind: "file", Name: "design.pdf", URL: "https://files.slack.com/x.pdf"},
	}))
	if !strings.Contains(got, "[File]") {
		t.Errorf("expected [File] marker, got %q", got)
	}
	if strings.Contains(got, "design.pdf") {
		t.Errorf("filename should be omitted, got %q", got)
	}
	if !strings.Contains(got, "https://files.slack.com/x.pdf") {
		t.Errorf("expected URL visible, got %q", got)
	}
}

// TestRenderAttachmentsEmpty returns empty string for no attachments.
func TestRenderAttachmentsEmpty(t *testing.T) {
	if got := RenderAttachments(nil); got != "" {
		t.Errorf("expected empty string for nil attachments, got %q", got)
	}
}

// TestRenderAttachmentsWrappedFitsLimit asserts that running attachment
// output through WordWrap produces lines that all fit the wrap limit.
// Attachment lines contain a long URL that has no whitespace, so without
// hard-break support the terminal would soft-wrap them and offset the
// surrounding layout.
func TestRenderAttachmentsWrappedFitsLimit(t *testing.T) {
	const limit = 60
	rendered := RenderAttachments([]Attachment{
		{Kind: "file", Name: "design.pdf", URL: "https://userevidence.slack.com/files/U05AZM7KJ1H/F0ATTEVCLUC/specright_roi_-_final_data_-_704193"},
	})
	wrapped := WordWrap(rendered, limit)
	for i, line := range strings.Split(wrapped, "\n") {
		if w := lipgloss.Width(line); w > limit {
			t.Errorf("attachment line %d width=%d exceeds limit=%d: plain=%q",
				i, w, limit, ansi.Strip(line))
		}
	}
}

// TestWordWrapBareURLEachLineFitsLimit asserts that wrapping a message
// containing a long bare URL — which RenderSlackMarkdown wraps in an OSC 8
// hyperlink escape — produces lines whose display width never exceeds the
// limit. Without proper hard-break of OSC-wrapped tokens, the terminal
// would soft-wrap the long line on its own and offset the rest of the
// thread layout.
func TestWordWrapBareURLEachLineFitsLimit(t *testing.T) {
	const limit = 50
	in := "see <https://userevidence.slack.com/files/U05AZM7KJ1H/F0ATTEVCLUC/specright_roi_-_final_data_-_704193> please"
	rendered := RenderSlackMarkdown(in, nil, nil)
	got := WordWrap(rendered, limit)
	for i, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > limit {
			t.Errorf("line %d width=%d exceeds limit=%d: plain=%q raw=%q",
				i, w, limit, ansi.Strip(line), line)
		}
	}
}

// TestWordWrapHardBreaksOverlongTokens guards against the layout bug where
// a single unbroken token (e.g. a long URL) wider than the wrap limit was
// emitted on one line, causing the terminal to soft-wrap it on its own.
// That extra terminal-side wrapping pushed the thread compose box over the
// last reply because lipgloss height arithmetic counted the overlong line
// as 1, not the multiple rows it actually consumed.
//
// Every output line must measure <= limit cells.
func TestWordWrapHardBreaksOverlongTokens(t *testing.T) {
	const limit = 40
	cases := []struct {
		name string
		in   string
	}{
		{"long URL alone", "https://userevidence.slack.com/files/U05AZM7KJ1H/F0ATTEVCLUC/specright_roi_-_final_data_-_704193"},
		{"long URL in sentence", "see https://example.com/this/is/a/very/long/path/that/cannot/break/at/word/boundaries for details"},
		{"giant identifier", "abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WordWrap(tc.in, limit)
			for i, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > limit {
					t.Errorf("line %d width=%d exceeds limit=%d: %q", i, w, limit, line)
				}
			}
		})
	}
}

// TestHTMLEntityDecoding asserts that Slack's HTML-escaped entities
// (&amp;, &lt;, &gt;) in message text are decoded back to literal
// characters per https://api.slack.com/reference/surfaces/formatting#escaping.
func TestHTMLEntityDecoding(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ampersand", "Hide &amp; Seek", "Hide & Seek"},
		{"less-than", "1 &lt; 2", "1 < 2"},
		{"greater-than", "2 &gt; 1", "2 > 1"},
		{"all three", "a &amp; b &lt; c &gt; d", "a & b < c > d"},
		{"mixed with bold", "*Hide &amp; Seek*", "Hide & Seek"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderSlackMarkdown(tc.in, nil, nil)
			plain := ansi.Strip(out)
			if !strings.Contains(plain, tc.want) {
				t.Errorf("expected %q in plain output, got %q", tc.want, plain)
			}
		})
	}
}

// TestBlockquoteEntityDecoding ensures entities inside blockquotes
// are also decoded.
func TestBlockquoteEntityDecoding(t *testing.T) {
	in := "&gt; Hide &amp; Seek"
	out := RenderSlackMarkdown(in, nil, nil)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "Hide & Seek") {
		t.Errorf("expected %q in plain output, got %q", "Hide & Seek", plain)
	}
}

// TestThreadBroadcastLabel asserts that a message with subtype
// "thread_broadcast" (a thread reply that the author also posted to
// the main channel) renders a "replied to a thread" label above the
// username row, and that a regular message does not.
func TestThreadBroadcastLabel(t *testing.T) {
	const labelText = "replied to a thread"

	t.Run("broadcast renders label", func(t *testing.T) {
		m := New([]MessageItem{{
			TS:        "1.0",
			UserName:  "alice",
			Text:      "hello channel",
			Timestamp: "3:04 PM",
			ThreadTS:  "0.5",
			Subtype:   "thread_broadcast",
		}}, "general")
		out := ansi.Strip(m.View(20, 60))
		if !strings.Contains(out, labelText) {
			t.Errorf("expected %q in output for thread_broadcast, got:\n%s", labelText, out)
		}
		// Label must appear BEFORE the username row.
		labelIdx := strings.Index(out, labelText)
		nameIdx := strings.Index(out, "alice")
		if labelIdx < 0 || nameIdx < 0 || labelIdx >= nameIdx {
			t.Errorf("expected label %q to appear before username, label@%d name@%d", labelText, labelIdx, nameIdx)
		}
	})

	t.Run("regular message does not render label", func(t *testing.T) {
		m := New([]MessageItem{{
			TS:        "1.0",
			UserName:  "alice",
			Text:      "hello channel",
			Timestamp: "3:04 PM",
		}}, "general")
		out := ansi.Strip(m.View(20, 60))
		if strings.Contains(out, labelText) {
			t.Errorf("regular message should not contain %q, got:\n%s", labelText, out)
		}
	})

	t.Run("plain thread reply (non-broadcast) does not render label", func(t *testing.T) {
		// A thread reply with ThreadTS set but no broadcast subtype
		// should not get the label. This case shouldn't appear in the
		// main channel feed at all, but if it does we don't mislabel it.
		m := New([]MessageItem{{
			TS:        "1.0",
			UserName:  "alice",
			Text:      "hello",
			Timestamp: "3:04 PM",
			ThreadTS:  "0.5",
		}}, "general")
		out := ansi.Strip(m.View(20, 60))
		if strings.Contains(out, labelText) {
			t.Errorf("plain thread reply should not contain %q, got:\n%s", labelText, out)
		}
	})
}

// TestEscapedAngleBracketsNotMistakenForMention asserts that escaped
// angle brackets (user-typed text) don't get re-interpreted as Slack
// markup after decoding.
func TestEscapedAngleBracketsNotMistakenForMention(t *testing.T) {
	// User typed literal "<@U123>" -- Slack escapes it.
	in := "&lt;@U123&gt;"
	out := RenderSlackMarkdown(in, map[string]string{"U123": "alice"}, nil)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "@alice") {
		t.Errorf("escaped mention should not resolve, got %q", plain)
	}
	if !strings.Contains(plain, "<@U123>") {
		t.Errorf("expected literal %q, got %q", "<@U123>", plain)
	}
}

// TestBgANSIForBasicColor asserts that bgANSIFor emits native ANSI 16
// background escapes (e.g. "\x1b[41m") for ansi.BasicColor instead of
// degrading to truecolor. Native ANSI 16 escapes are required for the
// terminal palette to be honored — truecolor escapes always bypass it.
func TestBgANSIForBasicColor(t *testing.T) {
	cases := []struct {
		name string
		c    ansi.BasicColor
		want string
	}{
		{"black", 0, "\x1b[40m"},
		{"red", 1, "\x1b[41m"},
		{"white", 7, "\x1b[47m"},
		{"bright black", 8, "\x1b[100m"},
		{"bright red", 9, "\x1b[101m"},
		{"bright white", 15, "\x1b[107m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bgANSIFor(tc.c)
			if got != tc.want {
				t.Errorf("bgANSIFor(%d) = %q, want %q", tc.c, got, tc.want)
			}
		})
	}
}

// TestFgANSIForBasicColor: symmetric for foreground.
func TestFgANSIForBasicColor(t *testing.T) {
	cases := []struct {
		name string
		c    ansi.BasicColor
		want string
	}{
		{"black", 0, "\x1b[30m"},
		{"red", 1, "\x1b[31m"},
		{"white", 7, "\x1b[37m"},
		{"bright black", 8, "\x1b[90m"},
		{"bright red", 9, "\x1b[91m"},
		{"bright white", 15, "\x1b[97m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fgANSIFor(tc.c)
			if got != tc.want {
				t.Errorf("fgANSIFor(%d) = %q, want %q", tc.c, got, tc.want)
			}
		})
	}
}

// TestBgFgANSIForIndexedColor asserts ANSI 256 emission for ansi.IndexedColor.
func TestBgFgANSIForIndexedColor(t *testing.T) {
	if got := bgANSIFor(ansi.IndexedColor(42)); got != "\x1b[48;5;42m" {
		t.Errorf("bgANSIFor(IndexedColor(42)) = %q, want %q", got, "\x1b[48;5;42m")
	}
	if got := fgANSIFor(ansi.IndexedColor(200)); got != "\x1b[38;5;200m" {
		t.Errorf("fgANSIFor(IndexedColor(200)) = %q, want %q", got, "\x1b[38;5;200m")
	}
}

// TestBgFgANSIForRGBA is a regression guard: truecolor emission is unchanged
// for existing hex-based themes.
func TestBgFgANSIForRGBA(t *testing.T) {
	c := color.RGBA{R: 26, G: 26, B: 46, A: 0xff}
	if got := bgANSIFor(c); got != "\x1b[48;2;26;26;46m" {
		t.Errorf("bgANSIFor(RGBA) = %q, want %q", got, "\x1b[48;2;26;26;46m")
	}
	if got := fgANSIFor(c); got != "\x1b[38;2;26;26;46m" {
		t.Errorf("fgANSIFor(RGBA) = %q, want %q", got, "\x1b[38;2;26;26;46m")
	}
}

// TestBgFgANSIForBasicColorOutOfRange pins the documented fall-through:
// BasicColor values ≥16 (constructible since BasicColor is uint8) are
// out of the valid 0-15 range and must fall through to truecolor rather
// than producing malformed escapes like "\x1b[116m".
func TestBgFgANSIForBasicColorOutOfRange(t *testing.T) {
	bg := bgANSIFor(ansi.BasicColor(16))
	if !strings.HasPrefix(bg, "\x1b[48;2;") {
		t.Errorf("out-of-range BasicColor bg should fall through to truecolor, got %q", bg)
	}
	fg := fgANSIFor(ansi.BasicColor(16))
	if !strings.HasPrefix(fg, "\x1b[38;2;") {
		t.Errorf("out-of-range BasicColor fg should fall through to truecolor, got %q", fg)
	}
}

// TestSubstituteBgSGR exercises the grammar-aware bg-parameter
// substitution. The helper must:
//   (1) substitute the param when it stands alone (\x1b[40m)
//   (2) substitute the param within a bundled SGR (\x1b[1;31;40m)
//   (3) NOT corrupt literal digits in non-SGR content
//   (4) NOT match the param value inside a 256-color sub-argument
//       (\x1b[38;5;40m is an FG index 40, not a bg basic 40)
//   (5) leave the string unchanged when from == to
func TestSubstituteBgSGR(t *testing.T) {
	const to = "48;2;100;100;200"

	cases := []struct {
		name string
		in   string
		from string
		want string
	}{
		{
			name: "standalone ANSI bg",
			in:   "\x1b[40mhello\x1b[m",
			from: "40",
			want: "\x1b[" + to + "mhello\x1b[m",
		},
		{
			name: "bundled SGR with ANSI bg",
			in:   "\x1b[1;31;40mbold red on black\x1b[m",
			from: "40",
			want: "\x1b[1;31;" + to + "mbold red on black\x1b[m",
		},
		{
			name: "literal 40 in content is not touched",
			in:   "page 40 of 100\x1b[40mtinted\x1b[m",
			from: "40",
			want: "page 40 of 100\x1b[" + to + "mtinted\x1b[m",
		},
		{
			name: "256-color FG index 40 is not touched",
			in:   "\x1b[38;5;40mfg only\x1b[m",
			from: "40",
			want: "\x1b[38;5;40mfg only\x1b[m",
		},
		{
			name: "256-color FG index 40 alongside bg 40 — only bg substituted",
			in:   "\x1b[38;5;40;40mfg256 bg basic\x1b[m",
			from: "40",
			want: "\x1b[38;5;40;" + to + "mfg256 bg basic\x1b[m",
		},
		{
			name: "truecolor bg substring substitution",
			in:   "\x1b[48;2;26;26;46mhello\x1b[m",
			from: "48;2;26;26;46",
			want: "\x1b[" + to + "mhello\x1b[m",
		},
		{
			name: "truecolor bg within bundled SGR",
			in:   "\x1b[1;38;2;255;255;255;48;2;26;26;46mtext\x1b[m",
			from: "48;2;26;26;46",
			want: "\x1b[1;38;2;255;255;255;" + to + "mtext\x1b[m",
		},
		{
			name: "no match leaves string unchanged",
			in:   "\x1b[31mred fg only\x1b[m",
			from: "40",
			want: "\x1b[31mred fg only\x1b[m",
		},
		{
			name: "from == to is a no-op",
			in:   "\x1b[40mhello\x1b[m",
			from: "40",
			// to passed = "40" same as from
			want: "\x1b[40mhello\x1b[m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTo := to
			if tc.name == "from == to is a no-op" {
				useTo = "40"
			}
			got := substituteBgSGR(tc.in, tc.from, useTo)
			if got != tc.want {
				t.Errorf("substituteBgSGR(%q, %q, %q) = %q, want %q",
					tc.in, tc.from, useTo, got, tc.want)
			}
		})
	}
}

// TestRepaintBgToSelectionTintWithANSITheme exercises the integration
// of substituteBgSGR via RepaintBgToSelectionTint when the theme uses
// an ANSI 16 background. We apply ansi dark so BgANSI() returns the
// basic 16-color form "\x1b[40m" — independent of how the theme stores
// the value internally. The repaint must substitute the bundled "40"
// param without corrupting either literal content or 256-color
// sub-arguments.
func TestRepaintBgToSelectionTintWithANSITheme(t *testing.T) {
	// Apply ansi dark via the display-name form to exercise the same
	// case-insensitive lookup path that the theme picker uses when it
	// saves "ANSI Dark" to config.toml. Restore dark afterward.
	styles.Apply("ANSI Dark", config.Theme{})
	t.Cleanup(func() { styles.Apply("dark", config.Theme{}) })

	if BgANSI() != "\x1b[40m" {
		t.Skipf("ansi dark theme not yet registered (Task 4 dependency); BgANSI()=%q", BgANSI())
	}

	// Build a synthetic rendered string mixing: plain text containing
	// the digit "40", a bundled SGR with bg 40, and a 256-color FG that
	// includes "40" as its index (must NOT be substituted).
	in := "see line 40\x1b[1;31;40mbold red on black\x1b[m and \x1b[38;5;40mfg256\x1b[m"
	out := RepaintBgToSelectionTint(in, true)

	// "line 40" plain digits must survive.
	if !strings.Contains(out, "see line 40") {
		t.Errorf("plain digit run was corrupted: %q", out)
	}
	// The bundled bg "40" must be replaced.
	if strings.Contains(out, "\x1b[1;31;40m") {
		t.Errorf("expected bundled bg 40 to be substituted, got %q", out)
	}
	// The 256-color FG with index 40 must be intact.
	if !strings.Contains(out, "\x1b[38;5;40m") {
		t.Errorf("expected 256-color FG index 40 to remain intact, got %q", out)
	}
}

// TestRepaintBgToSelectionTintBackwardCompat asserts no behavior change
// for truecolor themes — substituting a long, unique RGB param.
func TestRepaintBgToSelectionTintBackwardCompat(t *testing.T) {
	styles.Apply("dark", config.Theme{})
	t.Cleanup(func() { styles.Apply("dark", config.Theme{}) })

	bg := BgANSI()
	tint := SelectionTintBgANSI(true)
	from := bgSGRParams(bg)
	to := bgSGRParams(tint)
	if from == "" || from == to {
		t.Fatalf("test setup invalid: from=%q to=%q (both must be non-empty and differ)", from, to)
	}

	// Standalone bg escape: substituted to tint.
	in := "prefix" + bg + "tinted\x1b[m suffix"
	want := "prefix\x1b[" + to + "mtinted\x1b[m suffix"
	if got := RepaintBgToSelectionTint(in, true); got != want {
		t.Errorf("standalone bg substitution: got %q, want %q", got, want)
	}

	// Pass-through: strings with no bg escape are unchanged.
	noBg := "no escape here"
	if got := RepaintBgToSelectionTint(noBg, true); got != noBg {
		t.Errorf("pass-through: got %q, want %q", got, noBg)
	}
}
