// Package membership manages per-channel member sets: SQLite-backed
// persistence, eager fetch on channel switch, live event deltas, and
// external (Slack Connect) user resolution.
package membership

import (
	"context"
	"sync"
	"time"

	"github.com/gammons/slk/internal/cache"
)

// TTL bounds how stale cached membership can be before EnsureFresh
// triggers a background re-fetch. Spec: 24h.
const TTL = 24 * time.Hour

// ConversationMemberAPI is the slack-client subset Manager needs.
// Decoupled from *slackclient.Client for testability.
type ConversationMemberAPI interface {
	GetUsersInConversation(ctx context.Context, channelID string) ([]string, error)
}

// UserResolver is the userResolver subset Manager invokes to trigger
// external-user resolution for unknown IDs. nil-safe in early tasks;
// wired in Task 12.
type UserResolver interface {
	Request(userID string)
}

// PushFunc is invoked by Manager whenever a channel's membership has
// new data to surface to the UI. Wired in main.go to program.Send
// with a ui.ChannelMembershipMsg.
type PushFunc func(channelID string, memberIDs []string)

// Manager owns per-channel member state for one workspace.
type Manager struct {
	workspaceID string
	api         ConversationMemberAPI
	db          *cache.DB
	push        PushFunc
	resolver    UserResolver

	mu       sync.Mutex
	members  map[string]map[string]struct{} // channelID -> member set
	fetching map[string]struct{}            // in-flight sentinel by channelID
}

// New constructs a Manager bound to one workspace.
func New(workspaceID string, api ConversationMemberAPI, db *cache.DB, push PushFunc, resolver UserResolver) *Manager {
	return &Manager{
		workspaceID: workspaceID,
		api:         api,
		db:          db,
		push:        push,
		resolver:    resolver,
		members:     map[string]map[string]struct{}{},
		fetching:    map[string]struct{}{},
	}
}

// EnsureFresh loads (if needed) cached membership for a channel,
// pushes it to the UI, and triggers a background full-fetch if the
// cache is missing or older than TTL. Returns immediately; fetch is
// asynchronous.
func (m *Manager) EnsureFresh(ctx context.Context, channelID string) {
	m.loadIntoMemory(channelID)
	m.pushSnapshot(channelID)

	fetchedAt, ok, err := m.db.GetChannelMembershipMeta(m.workspaceID, channelID)
	if err != nil {
		// Treat read error as stale — better to refetch than to wedge.
		ok = false
	}
	fresh := ok && time.Since(time.Unix(fetchedAt, 0)) < TTL
	if fresh {
		return
	}
	go m.backgroundFetch(ctx, channelID)
}

// loadIntoMemory reads the cached member set into the in-memory map
// if not already present. Safe to call repeatedly.
func (m *Manager) loadIntoMemory(channelID string) {
	m.mu.Lock()
	_, have := m.members[channelID]
	m.mu.Unlock()
	if have {
		return
	}
	ids, err := m.db.ListChannelMembers(m.workspaceID, channelID)
	if err != nil {
		return
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	m.mu.Lock()
	if _, raced := m.members[channelID]; !raced {
		m.members[channelID] = set
	}
	m.mu.Unlock()
}

// pushSnapshot calls the push callback with the current in-memory
// member set for a channel.
func (m *Manager) pushSnapshot(channelID string) {
	m.mu.Lock()
	set := m.members[channelID]
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	if m.push != nil {
		m.push(channelID, ids)
	}
}

// backgroundFetch performs a single GetUsersInConversation call,
// persists the result via cache.ReplaceChannelMembers (which bumps
// last_full_fetch_at), updates in-memory state, and pushes to UI.
// Deduped by per-channel in-flight sentinel.
func (m *Manager) backgroundFetch(ctx context.Context, channelID string) {
	m.mu.Lock()
	if _, busy := m.fetching[channelID]; busy {
		m.mu.Unlock()
		return
	}
	m.fetching[channelID] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.fetching, channelID)
		m.mu.Unlock()
	}()

	ids, err := m.api.GetUsersInConversation(ctx, channelID)
	if err != nil {
		return
	}
	now := time.Now().Unix()
	if err := m.db.ReplaceChannelMembers(m.workspaceID, channelID, ids, now); err != nil {
		return
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	m.mu.Lock()
	m.members[channelID] = set
	m.mu.Unlock()
	m.pushSnapshot(channelID)
}
