package cache

import (
	"database/sql"
	"fmt"
)

// ReadState captures the per-channel read-state values that drive the
// unread dot and "new messages" line. It is the canonical type for
// passing read state across package boundaries.
type ReadState struct {
	LastReadTS string
	HasUnread  bool
}

// ChannelReadStateUpdate is one entry in a batched read-state write.
// LastReadTS == "" means "preserve the existing last_read_ts" (used by
// events that update has_unread only, e.g. new-message arrivals).
type ChannelReadStateUpdate struct {
	ChannelID  string
	LastReadTS string
	HasUnread  bool
}

// UpdateChannelReadState atomically updates the per-channel read state.
// If lastReadTS == "", the existing last_read_ts is preserved. This is
// the ONLY function permitted to modify read state after bootstrap.
func (db *DB) UpdateChannelReadState(channelID, lastReadTS string, hasUnread bool) error {
	return fmt.Errorf("not implemented")
}

// BatchUpdateChannelReadState writes multiple updates in a single
// transaction. Used by bootstrap and reconnect catch-up paths.
func (db *DB) BatchUpdateChannelReadState(updates []ChannelReadStateUpdate) error {
	return fmt.Errorf("not implemented")
}

// GetChannelReadState returns the read state for a single channel.
// A missing row yields a zero-valued ReadState and a nil error.
func (db *DB) GetChannelReadState(channelID string) (ReadState, error) {
	return ReadState{}, fmt.Errorf("not implemented")
}

// GetWorkspaceReadState returns channelID -> ReadState for every
// channel in the workspace. Single batched query. Called by the
// sidebar View() at render time.
func (db *DB) GetWorkspaceReadState(workspaceID string) (map[string]ReadState, error) {
	return nil, fmt.Errorf("not implemented")
}

// WorkspacesWithUnreads returns the set of workspace IDs with at least
// one has_unread=true channel. Used by the workspace rail.
func (db *DB) WorkspacesWithUnreads() ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}

var _ = sql.ErrNoRows // keep sql import for later use
