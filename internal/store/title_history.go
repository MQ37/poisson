package store

import (
	"fmt"
	"strings"
)

// TitleHistoryEntry is a title a session was previously set to, in the
// order SetSessionTitle recorded it.
type TitleHistoryEntry struct {
	Title     string
	CreatedAt int64
}

// TitleHistoryForSessions returns each session's title history, oldest
// first, keyed by session id. One query scoped to sessionIDs (typically a
// page just fetched by ListSessions) rather than the whole table — mirrors
// MessageCountsBySession's one-query-for-many-sessions shape, but bounded to
// the ids the caller actually needs. A session with no renames is simply
// absent from the returned map.
func (s *Store) TitleHistoryForSessions(sessionIDs []string) (map[string][]TitleHistoryEntry, error) {
	out := make(map[string][]TitleHistoryEntry)
	if len(sessionIDs) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sessionIDs)), ",")
	args := make([]interface{}, len(sessionIDs))
	for i, id := range sessionIDs {
		args[i] = id
	}

	rows, err := s.db.Query(
		`SELECT session_id, title, created_at FROM session_title_history
		 WHERE session_id IN (`+placeholders+`)
		 ORDER BY session_id, created_at ASC, rowid ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("title history for sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID string
		var e TitleHistoryEntry
		if err := rows.Scan(&sessionID, &e.Title, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan title history: %w", err)
		}
		out[sessionID] = append(out[sessionID], e)
	}
	return out, rows.Err()
}
