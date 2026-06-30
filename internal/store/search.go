package store

import (
	"fmt"
	"strings"
)

// SearchResult is a single FTS5 search hit.
type SearchResult struct {
	SessionID string
	MessageID string
	Role      string
	Snippet   string
	Rank      float64
}

// PrepareFTSQuery wraps a user query as an FTS5 phrase search so operators
// like OR, AND, and quotes are treated literally.
func PrepareFTSQuery(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return query
	}
	query = strings.ReplaceAll(query, `"`, `""`)
	return `"` + query + `"`
}

// Search runs an FTS5 MATCH query against messages_fts, joins back to the
// messages table to filter out inactive rows, and returns results ordered by
// relevance (rank). The snippet is the FTS5 highlight around the matched terms.
func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	query = PrepareFTSQuery(query)
	rows, err := s.db.Query(
		`SELECT fts.session_id, fts.message_id, fts.role,
		        snippet(messages_fts, 3, '[', ']', '...', 20),
		        fts.rank
		 FROM messages_fts fts
		 JOIN messages m ON m.id = fts.message_id
		 WHERE messages_fts MATCH ? AND m.deleted_at IS NULL AND m.compacted = 0
		 ORDER BY fts.rank
		 LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.SessionID, &r.MessageID, &r.Role, &r.Snippet, &r.Rank); err != nil {
			return nil, fmt.Errorf("search scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
