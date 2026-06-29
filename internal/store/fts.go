package store

import (
	"fmt"
	"strings"
)

// ftsEligible reports whether a message should be indexed for /search.
// Only user and assistant text (non-empty) is indexed — never tool rows.
func ftsEligible(role, text string) bool {
	if role != "user" && role != "assistant" {
		return false
	}
	return strings.TrimSpace(text) != ""
}

// reconcileFTS removes stale FTS rows (soft-deleted, compacted, tool, orphan).
func (s *Store) reconcileFTS() error {
	_, err := s.db.Exec(`
		DELETE FROM messages_fts
		WHERE message_id NOT IN (SELECT id FROM messages)
		   OR message_id IN (
		        SELECT id FROM messages
		        WHERE deleted_at IS NOT NULL
		           OR compacted = 1
		           OR role NOT IN ('user', 'assistant')
		      )`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM messages_fts WHERE trim(content_text) = ''`)
	return err
}

func (s *Store) deleteFTSForSoftDeleted(sessionID string, fromSeq int) error {
	_, err := s.db.Exec(`
		DELETE FROM messages_fts
		WHERE message_id IN (
			SELECT id FROM messages
			WHERE session_id = ? AND seq >= ? AND deleted_at IS NOT NULL
		)`, sessionID, fromSeq)
	if err != nil {
		return fmt.Errorf("delete fts soft-deleted: %w", err)
	}
	return nil
}

func (s *Store) deleteFTSCompactedThrough(sessionID string, upToSeq int) error {
	_, err := s.db.Exec(`
		DELETE FROM messages_fts
		WHERE message_id IN (
			SELECT id FROM messages
			WHERE session_id = ? AND seq <= ? AND compacted = 1
		)`, sessionID, upToSeq)
	if err != nil {
		return fmt.Errorf("delete fts compacted: %w", err)
	}
	return nil
}