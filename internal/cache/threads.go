package cache

import (
	"database/sql"
	"fmt"
	"sort"
)

// ThreadSummary is one row in the Threads view: a thread the user is
// involved in (authored, replied to, or @-mentioned in). Computed from
// the local cache; v1 has no Slack-side authoritative data.
type ThreadSummary struct {
	ChannelID    string
	ChannelName  string
	ChannelType  string // "channel" | "private" | "dm" | "group_dm"
	ThreadTS     string
	ParentUserID string
	ParentText   string
	ParentTS     string
	ReplyCount   int // number of replies (does not count the parent)
	LastReplyTS  string
	LastReplyBy  string
	Unread       bool
}

// ListInvolvedThreads returns threads in the given workspace where the user
// (selfUserID) authored the parent, posted a reply, or was @-mentioned
// (`<@UID>`) anywhere in the thread.
//
// Ordering: newest LastReplyTS first.
//
// Unread heuristic: LastReplyTS > channel.last_read_ts AND LastReplyBy != self.
// This is approximate; v2 will replace it with subscriptions.thread state.
func (db *DB) ListInvolvedThreads(workspaceID, selfUserID string) ([]ThreadSummary, error) {
	mention := "%<@" + selfUserID + ">%"

	const q = `
WITH involved AS (
  SELECT DISTINCT thread_ts, channel_id
  FROM messages
  WHERE workspace_id = ?
    AND thread_ts != ''
    AND is_deleted = 0
    AND (user_id = ? OR text LIKE ?)
)
SELECT
  m.channel_id,
  m.thread_ts,
  COALESCE(c.name, ''),
  COALESCE(c.type, ''),
  COALESCE(c.last_read_ts, ''),
  COALESCE((SELECT user_id FROM messages
              WHERE channel_id = m.channel_id AND ts = m.thread_ts AND is_deleted = 0), '')
    AS parent_user,
  COALESCE((SELECT text FROM messages
              WHERE channel_id = m.channel_id AND ts = m.thread_ts AND is_deleted = 0), '')
    AS parent_text,
  -- reply count excludes the parent (rows where ts == thread_ts)
  SUM(CASE WHEN m.ts != m.thread_ts THEN 1 ELSE 0 END) AS reply_count,
  MAX(m.ts) AS last_ts,
  (SELECT user_id FROM messages
     WHERE channel_id = m.channel_id AND thread_ts = m.thread_ts AND is_deleted = 0
     ORDER BY ts DESC LIMIT 1) AS last_by
FROM messages m
JOIN involved i ON i.thread_ts = m.thread_ts AND i.channel_id = m.channel_id
LEFT JOIN channels c ON c.id = m.channel_id
WHERE m.is_deleted = 0
GROUP BY m.channel_id, m.thread_ts
`

	rows, err := db.conn.Query(q, workspaceID, selfUserID, mention)
	if err != nil {
		return nil, fmt.Errorf("listing involved threads: %w", err)
	}
	defer rows.Close()

	var out []ThreadSummary
	for rows.Next() {
		var s ThreadSummary
		var lastRead string
		if err := rows.Scan(
			&s.ChannelID,
			&s.ThreadTS,
			&s.ChannelName,
			&s.ChannelType,
			&lastRead,
			&s.ParentUserID,
			&s.ParentText,
			&s.ReplyCount,
			&s.LastReplyTS,
			&s.LastReplyBy,
		); err != nil {
			return nil, fmt.Errorf("scanning thread summary: %w", err)
		}
		s.ParentTS = s.ThreadTS
		s.Unread = s.LastReplyTS > lastRead && s.LastReplyBy != selfUserID
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Order: newest LastReplyTS first. The Unread field is still
	// computed and returned so the UI can render the dot indicator,
	// but it no longer participates in ordering. The previous
	// "unread first" tier produced confusing results when
	// channels.last_read_ts was empty (string compare LastReplyTS >
	// "" was always true), pushing genuinely-recent activity below
	// older activity. See
	// docs/superpowers/specs/2026-05-11-reconnect-backfill-and-threads-sort-design.md
	// for context.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastReplyTS > out[j].LastReplyTS
	})
	return out, nil
}

// ThreadInvolvesUser reports whether the given thread (identified by
// workspaceID, channelID, threadTS) has any cached message authored
// by selfUserID or containing the angle-bracketed mention "<@selfUserID>".
// Mirrors the involvement predicate used by ListInvolvedThreads. Used
// by the reconnect backfill to filter which threads warrant a
// conversations.replies catch-up call.
func (db *DB) ThreadInvolvesUser(workspaceID, channelID, threadTS, selfUserID string) (bool, error) {
	mention := "%<@" + selfUserID + ">%"
	const q = `
SELECT 1 FROM messages
WHERE workspace_id = ? AND channel_id = ? AND thread_ts = ?
  AND is_deleted = 0
  AND (user_id = ? OR text LIKE ?)
LIMIT 1
`
	var one int
	err := db.conn.QueryRow(q, workspaceID, channelID, threadTS, selfUserID, mention).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking thread involvement: %w", err)
	}
	return true, nil
}
