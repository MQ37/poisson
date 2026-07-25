package store

import (
	"database/sql"
	"fmt"
)

// Compaction is a record of a compaction event.
type Compaction struct {
	ID           string
	SessionID    string
	MessageID    *string
	Summary      string
	TokensBefore int
	TokensAfter  int
	Cost         float64
	CreatedAt    int64
}

// RecordCompaction inserts a compaction record.
func (s *Store) RecordCompaction(c *Compaction) error {
	_, err := s.db.Exec(`INSERT INTO compactions
		(id, session_id, message_id, summary, tokens_before, tokens_after, cost, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, nilIfEmpty(c.MessageID), c.Summary,
		c.TokensBefore, c.TokensAfter, c.Cost, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("record compaction: %w", err)
	}
	return nil
}

// GetLastCompaction returns the most recent compaction record for a session.
func (s *Store) GetLastCompaction(sessionID string) (*Compaction, error) {
	var c Compaction
	var messageID sql.NullString
	// created_at has only whole-second resolution, so two compactions in the
	// same second (routine in tests, possible in practice) tie; break the tie
	// with rowid DESC so this always returns the most recently INSERTed row,
	// not whichever the tie happens to resolve to.
	err := s.db.QueryRow(`SELECT id, session_id, message_id, summary, tokens_before, tokens_after, cost, created_at
		FROM compactions WHERE session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionID).
		Scan(&c.ID, &c.SessionID, &messageID, &c.Summary,
			&c.TokensBefore, &c.TokensAfter, &c.Cost, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get last compaction: %w", err)
	}
	if messageID.Valid {
		v := messageID.String
		c.MessageID = &v
	}
	return &c, nil
}

// nilIfEmpty returns nil for a *string that's empty or nil.
func nilIfEmpty(s *string) interface{} {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}
