// internal/ui/mode_reaction_picker.go
//
// Reaction-picker mode key handler (Phase 5i).
//
// Forwards normalised keys to the reaction picker overlay. On a
// result (Enter on a selected emoji):
//   - Records frecent usage (skipped for removes).
//   - Applies an optimistic update to the message's reaction list
//     in both messagepane and threadPanel via
//     updateReactionOnMessage.
//   - Fires the API call asynchronously; the result lands as a
//     ReactionSentMsg and is folded back via reduceReactions
//     (Phase 4g).
//
// channelID / messageTS are captured BEFORE HandleKey runs because
// HandleKey may call Close internally, which resets those fields.
package ui

import (
	tea "charm.land/bubbletea/v2"
)

func handleReactionPickerMode(a *App, msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()

	switch msg.Key().Code {
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	// Capture values before HandleKey (which may call Close and
	// reset them).
	channelID := a.reactionPicker.ChannelID()
	messageTS := a.reactionPicker.MessageTS()

	result := a.reactionPicker.HandleKey(keyStr)

	if !a.reactionPicker.IsVisible() {
		// Esc was pressed.
		a.SetMode(ModeNormal)
		return nil
	}

	if result == nil {
		return nil
	}

	emojiName := result.Emoji

	a.reactionPicker.Close()
	a.SetMode(ModeNormal)

	// Record frecent usage on add (not remove).
	if !result.Remove {
		a.reactions.RecordFrecent(emojiName)
	}

	// Optimistic update.
	a.updateReactionOnMessage(channelID, messageTS, emojiName, a.currentUserID, result.Remove)

	// Fire API call.
	if result.Remove {
		return func() tea.Msg {
			err := a.reactions.Remove(channelID, messageTS, emojiName)
			return ReactionSentMsg{Err: err}
		}
	}
	return func() tea.Msg {
		err := a.reactions.Add(channelID, messageTS, emojiName)
		return ReactionSentMsg{Err: err}
	}
}
