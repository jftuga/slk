# List Message Reactions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `L` keybinding that opens a read-only modal listing every reaction on the selected message (or thread reply), grouped by emoji, with the display names of the users who reacted.

**Architecture:** Surface the per-user reaction data that already exists in the local cache by adding a `UserIDs` field to the UI's `messages.ReactionItem`, populating it at the cache→UI conversion seams in `cmd/slk/main.go` and maintaining it in the live websocket update paths. A new `internal/ui/reactionsview` overlay package (modeled on the existing `help`/`reactionpicker` modals) renders the grouped list. The App assembles the modal's data by resolving user IDs to names via the existing `userNameFor` helper and marking the current user `(you)`.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2 (`charm.land/lipgloss/v2`), `github.com/kyokomi/emoji/v2`. Module path: `github.com/gammons/slk`. Tests run with `go test ./... -race` (Makefile target `test`).

**Reference (read before starting):**
- Spec: `docs/superpowers/specs/2026-06-08-list-message-reactions-design.md`
- Closest existing modal to copy: `internal/ui/help/model.go` (simple scrollable list overlay) and `internal/ui/reactionpicker/model.go` (emoji rendering, scrollbar).
- Modal wiring reference: `internal/ui/mode_help.go`, `internal/ui/mode_handlers.go`, `internal/ui/view_overlays.go`, `internal/ui/mode.go`.

---

## File Structure

**Created:**
- `internal/ui/reactionsview/model.go` — the modal: data types (`ReactionGroup`), `Model`, `Open/Close/IsVisible/HandleKey/ViewOverlay`, and rendering.
- `internal/ui/reactionsview/model_test.go` — unit tests for state + rendering.
- `internal/ui/mode_reactions_view.go` — the per-mode key handler that forwards keys to the modal.

**Modified:**
- `internal/ui/messages/model.go` — add `UserIDs []string` to `ReactionItem`; maintain it in `UpdateReaction`.
- `internal/ui/messages/model_test.go` — test `UpdateReaction` maintains `UserIDs`.
- `internal/ui/thread/model.go` — maintain `UserIDs` in `UpdateReaction`.
- `internal/ui/thread/model_test.go` — test thread `UpdateReaction` maintains `UserIDs`.
- `cmd/slk/main.go` — populate `UserIDs` at the four `messages.ReactionItem{...}` construction sites.
- `internal/ui/keys.go` — add `ListReactions` binding (`L`).
- `internal/ui/mode.go` — add `ModeReactionsView` const, `String()` case, `IsModalOverlay()` case.
- `internal/ui/mode_handlers.go` — register `ModeReactionsView: handleReactionsViewMode`.
- `internal/ui/app.go` — add `reactionsView *reactionsview.Model` field, construct it in `NewApp`, add `openReactionsView()` + `buildReactionGroups()` helpers.
- `internal/ui/mode_normal.go` — dispatch the `ListReactions` key.
- `internal/ui/view_overlays.go` — composite the modal in `applyOverlays`.
- `internal/ui/app_test.go` — integration test: `L` opens the modal with correct content; no-op when no reactions.

**Note on the help modal:** `help.FromKeyMap` derives entries from the `KeyMap` struct via reflection (`internal/ui/help/model.go:52`), so adding `ListReactions` to `KeyMap` automatically adds it to the help overlay. No separate help-list edit is needed.

---

## Task 1: Add `UserIDs` to `ReactionItem` and maintain it in live updates

**Files:**
- Modify: `internal/ui/messages/model.go:79-83` (struct), `internal/ui/messages/model.go:1114-1189` (`UpdateReaction`)
- Modify: `internal/ui/thread/model.go:798-841` (`UpdateReaction`)
- Test: `internal/ui/messages/model_test.go`, `internal/ui/thread/model_test.go`

- [ ] **Step 1: Write the failing test (messages)**

Add to `internal/ui/messages/model_test.go`:

```go
func TestUpdateReactionMaintainsUserIDs(t *testing.T) {
	// messages.New(msgs, channelName) returns a value Model; m is addressable
	// so the pointer-receiver methods below work.
	m := New([]MessageItem{{TS: "100.0", Text: "hi"}}, "general")

	// Add a reaction by user U1 -> creates the group with U1.
	m.UpdateReaction("100.0", "thumbsup", "U1", false)
	msg, _ := m.SelectedMessage()
	if len(msg.Reactions) != 1 {
		t.Fatalf("want 1 reaction, got %d", len(msg.Reactions))
	}
	if got := msg.Reactions[0].UserIDs; len(got) != 1 || got[0] != "U1" {
		t.Fatalf("want UserIDs [U1], got %v", got)
	}

	// Add same emoji by U2 -> appends to the same group.
	m.UpdateReaction("100.0", "thumbsup", "U2", false)
	msg, _ = m.SelectedMessage()
	if got := msg.Reactions[0].UserIDs; len(got) != 2 || got[1] != "U2" {
		t.Fatalf("want UserIDs [U1 U2], got %v", got)
	}

	// Remove U1 -> group remains with U2.
	m.UpdateReaction("100.0", "thumbsup", "U1", true)
	msg, _ = m.SelectedMessage()
	if got := msg.Reactions[0].UserIDs; len(got) != 1 || got[0] != "U2" {
		t.Fatalf("want UserIDs [U2] after remove, got %v", got)
	}

	// Remove U2 -> group disappears (count hits 0).
	m.UpdateReaction("100.0", "thumbsup", "U2", true)
	msg, _ = m.SelectedMessage()
	if len(msg.Reactions) != 0 {
		t.Fatalf("want 0 reactions after all removed, got %d", len(msg.Reactions))
	}
}
```

If `New()` / `SetMessages` signatures differ from this, match the existing patterns already used elsewhere in `model_test.go` (search for `SetMessages(` in that file).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ui/messages/ -run TestUpdateReactionMaintainsUserIDs -v`
Expected: FAIL — `ReactionItem` has no field `UserIDs` (compile error).

- [ ] **Step 3: Add the `UserIDs` field**

In `internal/ui/messages/model.go`, change the struct (lines 79-83) to:

```go
type ReactionItem struct {
	Emoji      string   // emoji name without colons, e.g. "thumbsup"
	Count      int
	HasReacted bool     // whether the current user has reacted with this emoji
	UserIDs    []string // user IDs who reacted with this emoji (cache-sourced)
}
```

- [ ] **Step 4: Maintain `UserIDs` in `messages.Model.UpdateReaction`**

In `internal/ui/messages/model.go`, inside `UpdateReaction` (lines 1128-1161), update the four branches.

In the `remove` branch, replace the inner block (lines 1131-1142) with:

```go
				for j, r := range msg.Reactions {
					if r.Emoji == emojiName {
						r.Count--
						r.UserIDs = removeUserID(r.UserIDs, userID)
						if r.Count <= 0 {
							m.messages[i].Reactions = append(msg.Reactions[:j], msg.Reactions[j+1:]...)
						} else {
							r.HasReacted = false
							m.messages[i].Reactions[j] = r
						}
						break
					}
				}
```

In the `else` (add) branch, replace the block (lines 1144-1160) with:

```go
				found := false
				for j, r := range msg.Reactions {
					if r.Emoji == emojiName {
						r.Count++
						r.HasReacted = true
						r.UserIDs = appendUserID(r.UserIDs, userID)
						m.messages[i].Reactions[j] = r
						found = true
						break
					}
				}
				if !found {
					m.messages[i].Reactions = append(m.messages[i].Reactions, ReactionItem{
						Emoji:      emojiName,
						Count:      1,
						HasReacted: true,
						UserIDs:    appendUserID(nil, userID),
					})
				}
```

Then add these helpers near the bottom of `internal/ui/messages/model.go` (package-level functions):

```go
// appendUserID returns ids with userID appended, skipping empty IDs and
// de-duplicating so repeated reaction events don't double-count a user.
func appendUserID(ids []string, userID string) []string {
	if userID == "" {
		return ids
	}
	for _, id := range ids {
		if id == userID {
			return ids
		}
	}
	return append(ids, userID)
}

// removeUserID returns ids without userID (all occurrences).
func removeUserID(ids []string, userID string) []string {
	if userID == "" || len(ids) == 0 {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id != userID {
			out = append(out, id)
		}
	}
	return out
}
```

- [ ] **Step 5: Run the messages test to verify it passes**

Run: `go test ./internal/ui/messages/ -run TestUpdateReactionMaintainsUserIDs -v`
Expected: PASS

- [ ] **Step 6: Write the failing test (thread)**

Add to `internal/ui/thread/model_test.go` (note: thread uses `messages.ReactionItem`; check the file's existing imports — `messages` is already imported there):

```go
func TestThreadUpdateReactionMaintainsUserIDs(t *testing.T) {
	m := New()
	// SetThread(parent, replies, channelID, threadTS); the cursor defaults to
	// the last reply, so the single reply below becomes the selected one.
	m.SetThread(
		messages.MessageItem{TS: "199.0", Text: "parent"},
		[]messages.MessageItem{{TS: "200.0", Text: "reply"}},
		"C1", "199.0",
	)

	m.UpdateReaction("200.0", "eyes", "U1", false)
	reply := m.SelectedReply()
	if reply == nil || len(reply.Reactions) != 1 {
		t.Fatalf("want 1 reaction on selected reply")
	}
	if got := reply.Reactions[0].UserIDs; len(got) != 1 || got[0] != "U1" {
		t.Fatalf("want UserIDs [U1], got %v", got)
	}

	m.UpdateReaction("200.0", "eyes", "U1", true)
	reply = m.SelectedReply()
	if reply != nil && len(reply.Reactions) != 0 {
		t.Fatalf("want 0 reactions after remove, got %d", len(reply.Reactions))
	}
}
```

Match `New()` / `SetThread` / selection setup to the existing patterns in `thread/model_test.go` (search for `SetThread(` — confirm the constructor name and arg order).

- [ ] **Step 7: Run the thread test to verify it fails**

Run: `go test ./internal/ui/thread/ -run TestThreadUpdateReactionMaintainsUserIDs -v`
Expected: FAIL — `UserIDs` not maintained (assertions fail).

- [ ] **Step 8: Maintain `UserIDs` in `thread.Model.UpdateReaction`**

In `internal/ui/thread/model.go`, inside `UpdateReaction` (lines 800-840), apply the same edits as Step 4 but against `reply.Reactions` / `m.replies[i]`. The thread package can reuse helpers from the messages package — add `messages.AppendUserID` / `messages.RemoveUserID` exported wrappers, OR duplicate the two small helpers locally in `thread/model.go`. To avoid exporting, duplicate them locally:

```go
func appendUserID(ids []string, userID string) []string {
	if userID == "" {
		return ids
	}
	for _, id := range ids {
		if id == userID {
			return ids
		}
	}
	return append(ids, userID)
}

func removeUserID(ids []string, userID string) []string {
	if userID == "" || len(ids) == 0 {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id != userID {
			out = append(out, id)
		}
	}
	return out
}
```

Remove branch (lines 803-814) becomes:

```go
				for j, r := range reply.Reactions {
					if r.Emoji == emojiName {
						r.Count--
						r.UserIDs = removeUserID(r.UserIDs, userID)
						if r.Count <= 0 {
							m.replies[i].Reactions = append(reply.Reactions[:j], reply.Reactions[j+1:]...)
						} else {
							r.HasReacted = false
							m.replies[i].Reactions[j] = r
						}
						break
					}
				}
```

Add branch (lines 816-832) becomes:

```go
				found := false
				for j, r := range reply.Reactions {
					if r.Emoji == emojiName {
						r.Count++
						r.HasReacted = true
						r.UserIDs = appendUserID(r.UserIDs, userID)
						m.replies[i].Reactions[j] = r
						found = true
						break
					}
				}
				if !found {
					m.replies[i].Reactions = append(m.replies[i].Reactions, messages.ReactionItem{
						Emoji:      emojiName,
						Count:      1,
						HasReacted: true,
						UserIDs:    appendUserID(nil, userID),
					})
				}
```

- [ ] **Step 9: Run the thread test to verify it passes**

Run: `go test ./internal/ui/thread/ -run TestThreadUpdateReactionMaintainsUserIDs -v`
Expected: PASS

- [ ] **Step 10: Run both packages' full suites**

Run: `go test ./internal/ui/messages/ ./internal/ui/thread/ -race`
Expected: PASS (no regressions).

- [ ] **Step 11: Commit**

```bash
git add internal/ui/messages/model.go internal/ui/messages/model_test.go internal/ui/thread/model.go internal/ui/thread/model_test.go
git commit -m "feat(reactions): track reacting user IDs on ReactionItem"
```

---

## Task 2: Populate `UserIDs` at the cache→UI conversion seams

**Files:**
- Modify: `cmd/slk/main.go` at the four `messages.ReactionItem{...}` construction sites: lines 2166, 2422, 2577, 2668.

There are no unit tests for `cmd/slk/main.go`; verification is by build + the integration test in Task 6. The two source shapes:
- Cache path (line 2422): the loop variable is a `cache.ReactionRow` named `r` with `r.UserIDs`.
- API paths (lines 2166, 2577, 2668): the loop variable is a `slack.ItemReaction` named `r` with `r.Users` (the same field already used to compute `hasReacted`).

- [ ] **Step 1: Update the cache-path construction (line 2422)**

Change the literal at `cmd/slk/main.go:2422-2426` to include `UserIDs`:

```go
			reactions = append(reactions, messages.ReactionItem{
				Emoji:      r.Emoji,
				Count:      r.Count,
				HasReacted: hasReacted,
				UserIDs:    r.UserIDs,
			})
```

- [ ] **Step 2: Update the three API-path constructions (lines 2166, 2577, 2668)**

At each of `cmd/slk/main.go:2166`, `:2577`, `:2668`, add `UserIDs: r.Users,` to the `messages.ReactionItem{...}` literal. Before editing each, read the surrounding ~10 lines to confirm the loop variable name and that `r.Users` is the field in scope (it is the same slice already used to compute `HasReacted`). Example shape:

```go
			reactions = append(reactions, messages.ReactionItem{
				Emoji:      r.Name,
				Count:      r.Count,
				HasReacted: hasReacted,
				UserIDs:    r.Users,
			})
```

(Keep the existing `Emoji`/`Count`/`HasReacted` field expressions exactly as they already are at each site — only add the `UserIDs` line.)

- [ ] **Step 3: Build the whole project**

Run: `go build ./...`
Expected: builds with no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/slk/main.go
git commit -m "feat(reactions): populate ReactionItem.UserIDs from cache and API"
```

---

## Task 3: Create the `reactionsview` modal — state + key handling

**Files:**
- Create: `internal/ui/reactionsview/model.go`
- Test: `internal/ui/reactionsview/model_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/ui/reactionsview/model_test.go`:

```go
package reactionsview

import "testing"

func sampleGroups() []ReactionGroup {
	return []ReactionGroup{
		{Emoji: "thumbsup", Users: []string{"Alice", "Bob", "You (you)"}},
		{Emoji: "eyes", Users: []string{"Carol"}},
	}
}

func TestOpenCloseVisibility(t *testing.T) {
	m := New()
	if m.IsVisible() {
		t.Fatal("new model should not be visible")
	}
	m.Open(sampleGroups())
	if !m.IsVisible() {
		t.Fatal("Open should make the model visible")
	}
	m.Close()
	if m.IsVisible() {
		t.Fatal("Close should hide the model")
	}
}

func TestHandleKeyEscapeCloses(t *testing.T) {
	m := New()
	m.Open(sampleGroups())
	m.HandleKey("esc")
	if m.IsVisible() {
		t.Fatal("esc should close the modal")
	}
}

func TestHandleKeyScrollClamps(t *testing.T) {
	m := New()
	m.Open(sampleGroups())
	// Scrolling up at the top stays at 0.
	m.HandleKey("up")
	if m.Offset() != 0 {
		t.Fatalf("offset should clamp to 0 at top, got %d", m.Offset())
	}
	// Many downs should not exceed the max offset (never negative).
	for i := 0; i < 100; i++ {
		m.HandleKey("down")
	}
	if m.Offset() < 0 {
		t.Fatalf("offset should never be negative, got %d", m.Offset())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ui/reactionsview/ -v`
Expected: FAIL — package/types do not exist (compile error).

- [ ] **Step 3: Implement the model (state + keys only; rendering stub)**

Create `internal/ui/reactionsview/model.go`:

```go
// Package reactionsview provides a read-only modal overlay that lists the
// reactions on a message, grouped by emoji, with the display names of the
// users who reacted. Data is supplied by the App (assembled from the cached
// per-user reaction data); the modal does not fetch anything itself.
package reactionsview

// ReactionGroup is one emoji and the resolved display names of the users who
// reacted with it. The current user's name is expected to already carry a
// "(you)" suffix when assembled by the caller.
type ReactionGroup struct {
	Emoji string
	Users []string
}

// Model is the reactions-list overlay state.
type Model struct {
	groups  []ReactionGroup
	visible bool
	offset  int // scroll offset in rendered content lines
	maxOff  int // last computed maximum offset (set during render)
}

// New creates an empty, hidden modal.
func New() *Model { return &Model{} }

// Open shows the modal for the given reaction groups and resets scroll.
func (m *Model) Open(groups []ReactionGroup) {
	m.groups = groups
	m.offset = 0
	m.maxOff = 0
	m.visible = true
}

// Close hides the modal and clears state.
func (m *Model) Close() {
	m.visible = false
	m.groups = nil
	m.offset = 0
	m.maxOff = 0
}

// IsVisible reports whether the modal is showing.
func (m *Model) IsVisible() bool { return m.visible }

// Offset returns the current scroll offset (exported for tests).
func (m *Model) Offset() int { return m.offset }

// HandleKey processes a key for the modal. esc or L closes it; up/down and
// j/k scroll. Scroll is clamped to [0, maxOff], where maxOff is recomputed on
// each render; before the first render maxOff is 0 so scrolling is inert.
func (m *Model) HandleKey(keyStr string) {
	switch keyStr {
	case "esc", "escape", "q", "L":
		m.Close()
	case "up", "k":
		if m.offset > 0 {
			m.offset--
		}
	case "down", "j":
		if m.offset < m.maxOff {
			m.offset++
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/ui/reactionsview/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/reactionsview/model.go internal/ui/reactionsview/model_test.go
git commit -m "feat(reactionsview): add modal state and key handling"
```

---

## Task 4: Render the modal

**Files:**
- Modify: `internal/ui/reactionsview/model.go` (add `ViewOverlay`, `renderBox`, emoji helper)
- Test: `internal/ui/reactionsview/model_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/ui/reactionsview/model_test.go`:

```go
import "strings" // add to the import block at the top of the file

func TestViewOverlayRendersNamesAndCounts(t *testing.T) {
	m := New()
	out := m.ViewOverlay(80, 24, "background")
	if out != "background" {
		t.Fatal("hidden modal should return background unchanged")
	}

	m.Open(sampleGroups())
	out = m.ViewOverlay(80, 24, strings.Repeat("\n", 24))
	for _, want := range []string{"Reactions", "Alice", "Bob", "Carol", "You (you)", "(3)", "(1)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered modal missing %q\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ui/reactionsview/ -run TestViewOverlayRendersNamesAndCounts -v`
Expected: FAIL — `ViewOverlay` not defined (compile error).

- [ ] **Step 3: Implement rendering**

Add to the import block at the top of `internal/ui/reactionsview/model.go`:

```go
import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	emoji "github.com/kyokomi/emoji/v2"

	slkemoji "github.com/gammons/slk/internal/emoji"
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/overlay"
	"github.com/gammons/slk/internal/ui/styles"
)
```

(Keep the existing package doc comment above the import block.)

Add these methods/functions to `internal/ui/reactionsview/model.go`:

```go
// emojiGlyph renders an emoji name as a Unicode glyph when it is a
// composition-safe single codepoint, falling back to the :shortcode: form
// (same primitive the reaction picker uses). Workspace custom emoji are not in
// the built-in CodeMap and fall back to :name: which is the desired behavior.
func emojiGlyph(name string) string {
	code := ":" + name + ":"
	if u, ok := emoji.CodeMap()[code]; ok {
		u = strings.TrimRight(u, " ")
		if slkemoji.ShouldRenderUnicode(u) {
			return u
		}
	}
	return code
}

// contentLines builds the full (unwindowed) list of rendered content lines:
// an emoji header per group followed by one indented line per user.
func (m *Model) contentLines(bg lipgloss.Color, innerWidth int) []string {
	headerStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.Primary).Bold(true)
	userStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.TextPrimary)

	var lines []string
	for _, g := range m.groups {
		header := emojiGlyph(g.Emoji) + "  (" + strconv.Itoa(len(g.Users)) + ")"
		lines = append(lines, headerStyle.Width(innerWidth).Render(header))
		for _, u := range g.Users {
			lines = append(lines, userStyle.Width(innerWidth).Render("  "+u))
		}
	}
	return lines
}

// ViewOverlay composites the modal onto background. Returns background
// unchanged when hidden.
func (m *Model) ViewOverlay(termWidth, termHeight int, background string) string {
	if !m.visible {
		return background
	}
	box := m.renderBox(termWidth, termHeight)
	if box == "" {
		return background
	}
	result := overlay.DimmedOverlay(termWidth, termHeight, background, box, 0.5)
	lines := strings.Split(result, "\n")
	if len(lines) > termHeight {
		lines = lines[:termHeight]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderBox(termWidth, termHeight int) string {
	if !m.visible {
		return ""
	}

	overlayWidth := termWidth * 6 / 10
	if overlayWidth < 30 {
		overlayWidth = 30
	}
	if overlayWidth > 60 {
		overlayWidth = 60
	}
	if overlayWidth > termWidth-2 {
		overlayWidth = termWidth - 2
	}
	innerWidth := overlayWidth - 4 // border + padding

	bg := styles.Background

	title := lipgloss.NewStyle().
		Bold(true).
		Background(bg).
		Foreground(styles.Primary).
		Render("Reactions")

	all := m.contentLines(bg, innerWidth)

	// Visible window: leave headroom for title, blank, footer (~6 lines).
	maxVisible := termHeight - 8
	if maxVisible < 3 {
		maxVisible = 3
	}
	if maxVisible > 24 {
		maxVisible = 24
	}

	m.maxOff = len(all) - maxVisible
	if m.maxOff < 0 {
		m.maxOff = 0
	}
	if m.offset > m.maxOff {
		m.offset = m.maxOff
	}

	end := m.offset + maxVisible
	if end > len(all) {
		end = len(all)
	}
	window := all[m.offset:end]

	footer := lipgloss.NewStyle().
		Background(bg).
		Foreground(styles.TextMuted).
		Render("\u2191/\u2193 scroll   esc close")

	content := title + "\n\n" + strings.Join(window, "\n") + "\n\n" + footer

	// Re-paint modal bg+fg after every ANSI reset so trailing/unstyled cells
	// don't leak the dimmed app behind the overlay (same as help/picker).
	content = messages.ReapplyBgAfterResets(content, messages.BgANSI()+messages.FgANSI())

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Primary).
		BorderBackground(bg).
		Background(bg).
		Padding(1, 1).
		Width(overlayWidth).
		Render(content)
}
```

Note on `styles.Background` type: in `renderBox` it is assigned to `bg` and passed to `contentLines(bg lipgloss.Color, ...)`. Confirm the type of `styles.Background` (grep `var Background` in `internal/ui/styles/`) and make the `contentLines` parameter type match it (it is the same value `help`/`reactionpicker` pass to `lipgloss` `Background(...)`). If `styles.Background` is not a `lipgloss.Color`, change the `contentLines` signature parameter type to match its actual type.

- [ ] **Step 4: Run the render test to verify it passes**

Run: `go test ./internal/ui/reactionsview/ -run TestViewOverlayRendersNamesAndCounts -v`
Expected: PASS

- [ ] **Step 5: Run the full package suite**

Run: `go test ./internal/ui/reactionsview/ -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/ui/reactionsview/model.go internal/ui/reactionsview/model_test.go
git commit -m "feat(reactionsview): render grouped reactions with users"
```

---

## Task 5: Wire the modal into the App (keybinding, mode, dispatch, overlay)

**Files:**
- Modify: `internal/ui/keys.go:6-48` (struct field), `:50-93` (default binding)
- Modify: `internal/ui/mode.go` (const, `String()`, `IsModalOverlay()`)
- Create: `internal/ui/mode_reactions_view.go`
- Modify: `internal/ui/mode_handlers.go:50-63` (register handler)
- Modify: `internal/ui/app.go` (field ~line 199, construction ~line 370, new helpers near `openPickerFromMessage` ~line 664)
- Modify: `internal/ui/mode_normal.go` (dispatch)
- Modify: `internal/ui/view_overlays.go` (`applyOverlays`)

- [ ] **Step 1: Add the keybinding**

In `internal/ui/keys.go`, add a field to the `KeyMap` struct (after `SaveThread`, line 47):

```go
	ListReactions       key.Binding
```

In `DefaultKeyMap()` (before the closing `}` of the returned literal, after the `SaveThread` line 92), add:

```go
		ListReactions:       key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "list reactions")),
```

- [ ] **Step 2: Add the mode**

In `internal/ui/mode.go`:
- Add `ModeReactionsView` to the `const` block (after `ModeNewMessage`, line 19):

```go
	ModeReactionsView
```

- Add it to `IsModalOverlay()`'s `case` list (after `ModeNewMessage`, line 40):

```go
		ModeNewMessage,
		ModeReactionsView:
```

(i.e. add `ModeReactionsView` to the existing comma-separated case.)

- Add a `String()` case (after the `ModeNewMessage` case, line 74):

```go
	case ModeReactionsView:
		return "REACTIONS"
```

- [ ] **Step 3: Add the App field and construct it**

In `internal/ui/app.go`, add a field near the reaction picker field (after line 199 `reactionPicker *reactionpicker.Model`):

```go
	reactionsView  *reactionsview.Model
```

In `NewApp` (near line 370 `reactionPicker: reactionpicker.New(),`), add:

```go
		reactionsView:        reactionsview.New(),
```

Add the import to `internal/ui/app.go`'s import block:

```go
	"github.com/gammons/slk/internal/ui/reactionsview"
```

- [ ] **Step 4: Add the App open + data-assembly helpers**

In `internal/ui/app.go`, add near `openPickerFromMessage` (after line 698):

```go
// openReactionsView opens the read-only reactions list for the selected
// message (main pane) or selected reply (thread pane). It is a no-op when
// nothing is selected or the target has no reactions.
func (a *App) openReactionsView() tea.Cmd {
	var reactions []messages.ReactionItem
	switch a.focusedPanel {
	case PanelMessages:
		msg, ok := a.messagepane.SelectedMessage()
		if !ok {
			return nil
		}
		reactions = msg.Reactions
		a.messagepane.ExitReactionNav()
	case PanelThread:
		reply := a.threadPanel.SelectedReply()
		if reply == nil {
			return nil
		}
		reactions = reply.Reactions
		a.threadPanel.ExitReactionNav()
	default:
		return nil
	}
	if len(reactions) == 0 {
		return nil
	}
	a.reactionsView.Open(a.buildReactionGroups(reactions))
	a.SetMode(ModeReactionsView)
	return nil
}

// buildReactionGroups resolves each reaction's user IDs to display names,
// marking the current user with a "(you)" suffix.
func (a *App) buildReactionGroups(reactions []messages.ReactionItem) []reactionsview.ReactionGroup {
	groups := make([]reactionsview.ReactionGroup, 0, len(reactions))
	for _, r := range reactions {
		users := make([]string, 0, len(r.UserIDs))
		for _, uid := range r.UserIDs {
			name := a.userNameFor(uid)
			if uid == a.currentUserID {
				name += " (you)"
			}
			users = append(users, name)
		}
		groups = append(groups, reactionsview.ReactionGroup{Emoji: r.Emoji, Users: users})
	}
	return groups
}
```

Confirm `messages` is already imported in `app.go` (it is — `messages.ReactionItem` / `messages.MessageItem` are used throughout). Confirm `PanelMessages` / `PanelThread` are the focus constants used elsewhere in this file (they are, e.g. `mode_normal.go:36-40`).

- [ ] **Step 5: Dispatch the key in normal mode**

In `internal/ui/mode_normal.go`, add a case (after the `ReactionNav` case, line 188):

```go
	case key.Matches(msg, a.keys.ListReactions):
		return a.openReactionsView()
```

- [ ] **Step 6: Create the mode handler**

Create `internal/ui/mode_reactions_view.go`:

```go
// internal/ui/mode_reactions_view.go
//
// Reactions-view key handler. Forwards normalised keys to the
// reactionsview overlay (esc/q/L close, up/down scroll), then drops back
// to Normal mode when the overlay reports itself invisible.
package ui

import (
	tea "charm.land/bubbletea/v2"
)

func handleReactionsViewMode(a *App, msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	}
	a.reactionsView.HandleKey(keyStr)
	if !a.reactionsView.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return nil
}
```

- [ ] **Step 7: Register the handler**

In `internal/ui/mode_handlers.go`, add to the `modeHandlers` map (after `ModeHelp`, line 62):

```go
	ModeReactionsView:        handleReactionsViewMode,
```

- [ ] **Step 8: Composite the overlay**

In `internal/ui/view_overlays.go`:
- In `applyOverlays` (after the help block, lines 61-63), add:

```go
	if a.reactionsView.IsVisible() {
		screen = a.reactionsView.ViewOverlay(a.width, a.height, screen)
	}
```

- In `overlayActive` (add to the `||` chain, after `a.help.IsVisible()`, line 82):

```go
		a.reactionsView.IsVisible() ||
```

- [ ] **Step 9: Build**

Run: `go build ./...`
Expected: builds with no errors.

- [ ] **Step 10: Run the ui package tests**

Run: `go test ./internal/ui/... -race`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/ui/keys.go internal/ui/mode.go internal/ui/mode_reactions_view.go internal/ui/mode_handlers.go internal/ui/app.go internal/ui/mode_normal.go internal/ui/view_overlays.go
git commit -m "feat(reactions): wire L keybinding to reactions list modal"
```

---

## Task 6: Integration test — `L` opens the modal; no-op when empty

**Files:**
- Modify: `internal/ui/app_test.go`

- [ ] **Step 1: Inspect existing App test helpers**

Read `internal/ui/app_test.go` to find the existing pattern for constructing an `App`, seeding messages into `messagepane`, setting focus to `PanelMessages`, and sending a key (search for `NewApp`, `SetMessages`, `focusedPanel`, and how key messages are delivered — likely a helper that calls `dispatchModeKey` or `handleKey` with a `tea.KeyPressMsg`). Mirror that exact pattern in the new tests below; adjust constructor/seed calls to match.

- [ ] **Step 2: Write the test**

Add to `internal/ui/app_test.go` (adapt construction/seeding to the helpers found in Step 1):

```go
func TestListReactionsKeyOpensModal(t *testing.T) {
	app := newTestApp(t) // use whatever constructor the file already uses
	app.focusedPanel = PanelMessages
	app.messagepane.SetMessages([]messages.MessageItem{{
		TS:   "100.0",
		Text: "decision?",
		Reactions: []messages.ReactionItem{
			{Emoji: "thumbsup", Count: 2, UserIDs: []string{"U1", "U2"}},
		},
	}})
	app.SetUserNames(map[string]string{"U1": "Alice", "U2": "Bob"})

	cmd := app.openReactionsView()
	_ = cmd
	if !app.reactionsView.IsVisible() {
		t.Fatal("L should open the reactions modal when reactions exist")
	}
	if app.mode != ModeReactionsView {
		t.Fatalf("mode should be ModeReactionsView, got %v", app.mode)
	}

	out := app.reactionsView.ViewOverlay(80, 24, strings.Repeat("\n", 24))
	if !strings.Contains(out, "Alice") || !strings.Contains(out, "Bob") {
		t.Fatalf("modal should list reactor names, got:\n%s", out)
	}
}

func TestListReactionsNoOpWhenNoReactions(t *testing.T) {
	app := newTestApp(t)
	app.focusedPanel = PanelMessages
	app.messagepane.SetMessages([]messages.MessageItem{{TS: "100.0", Text: "no reactions"}})

	app.openReactionsView()
	if app.reactionsView.IsVisible() {
		t.Fatal("L should be a no-op when the message has no reactions")
	}
	if app.mode == ModeReactionsView {
		t.Fatal("mode should not change when there are no reactions")
	}
}
```

Notes:
- Use the file's real App constructor and user-name setter. If there is no `SetUserNames` on `App`, find how `a.userNames` is populated in tests (grep `userNames` in `app_test.go` / `app.go`) and use that path; unresolved IDs will fall back to the raw ID, which still satisfies the "names present" check if you assert on `"U1"`/`"U2"` instead.
- Ensure `strings` and `messages` are imported in `app_test.go` (they likely already are).

- [ ] **Step 3: Run the tests to verify they fail or pass appropriately**

Run: `go test ./internal/ui/ -run TestListReactions -v`
Expected: PASS (the implementation from Task 5 already exists). If a helper name is wrong, fix the test to match real helpers and re-run.

- [ ] **Step 4: Run the full suite with race**

Run: `go test ./... -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/app_test.go
git commit -m "test(reactions): integration test for L reactions modal"
```

---

## Task 7: Final verification

- [ ] **Step 1: Full build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 2: Full test suite**

Run: `make test`
Expected: all packages PASS.

- [ ] **Step 3: Lint**

Run: `make lint`
Expected: no new lint errors in the touched files. (If `golangci-lint` is not installed, skip and note it.)

- [ ] **Step 4: Manual smoke test (optional, requires a configured workspace)**

Run: `make run`, open a channel with a reacted-to message, press `L`. Confirm: the modal lists each emoji with its reactors; `(you)` appears next to your own name; `esc` closes; `L` on a message with no reactions does nothing; the same works on a selected thread reply.

---

## Self-Review Notes

- **Spec coverage:** trigger `L` (Task 5), both panes (Task 5 `openReactionsView` switch), cache-only data (Tasks 1-2 + `buildReactionGroups`), grouped layout with counts (Task 4), no-op when empty (Task 5/6), `(you)` marker + raw-ID fallback (Task 5 `buildReactionGroups` + `userNameFor`), help entry (automatic via `FromKeyMap`).
- **Type consistency:** `ReactionGroup{Emoji, Users}`, `ReactionItem.UserIDs`, helper names `appendUserID`/`removeUserID`, `openReactionsView`/`buildReactionGroups`, `handleReactionsViewMode`, `ModeReactionsView` used consistently across tasks.
- **Known follow-ups (out of scope, per spec):** no live async name resolution (raw-ID fallback is accepted); custom workspace emoji render as `:shortcode:` in the modal; no `reactions.get` API refresh.
