package store

import "fmt"

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

// nilIfEmpty returns nil for a *string that's empty or nil.
func nilIfEmpty(s *string) interface{} {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}
