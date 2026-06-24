package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// APICall represents a row in the api_calls table — one per provider HTTP
// request, storing exact usage and computed cost.
type APICall struct {
	ID               string
	SessionID        string
	Seq              int
	Model            string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Cost             float64
	CreatedAt        int64
}

// TokenBreakdown is the aggregate token usage for a session.
type TokenBreakdown struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	TotalCost        float64
	CallCount        int
}

// RecordAPICall inserts an api_calls row. ID and CreatedAt are populated
// if zero.
func (s *Store) RecordAPICall(call *APICall) error {
	if call.ID == "" {
		call.ID = newUUID()
	}
	if call.CreatedAt == 0 {
		call.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO api_calls
		 (id, session_id, seq, model, input_tokens, output_tokens,
		  cache_read_tokens, cache_write_tokens, cost, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		call.ID, call.SessionID, call.Seq, call.Model,
		call.InputTokens, call.OutputTokens,
		call.CacheReadTokens, call.CacheWriteTokens,
		call.Cost, call.CreatedAt)
	if err != nil {
		return fmt.Errorf("record api call: %w", err)
	}
	return nil
}

// GetLastAPICall returns the most recent api_calls row for a session
// (by created_at desc), or ErrNotFound.
func (s *Store) GetLastAPICall(sessionID string) (*APICall, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, seq, model, input_tokens, output_tokens,
		        cache_read_tokens, cache_write_tokens, cost, created_at
		 FROM api_calls WHERE session_id = ?
		 ORDER BY created_at DESC, seq DESC LIMIT 1`, sessionID)
	var c APICall
	err := row.Scan(
		&c.ID, &c.SessionID, &c.Seq, &c.Model,
		&c.InputTokens, &c.OutputTokens,
		&c.CacheReadTokens, &c.CacheWriteTokens,
		&c.Cost, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get last api call: %w", err)
	}
	return &c, nil
}

// GetSessionCost returns the total cost (SUM(cost)) for a session.
func (s *Store) GetSessionCost(sessionID string) (float64, error) {
	var cost float64
	row := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost), 0) FROM api_calls WHERE session_id = ?`,
		sessionID)
	if err := row.Scan(&cost); err != nil {
		return 0, fmt.Errorf("get session cost: %w", err)
	}
	return cost, nil
}

// GetTotalCost returns the total cost across all sessions.
func (s *Store) GetTotalCost() (float64, error) {
	var cost float64
	row := s.db.QueryRow(`SELECT COALESCE(SUM(cost), 0) FROM api_calls`)
	if err := row.Scan(&cost); err != nil {
		return 0, fmt.Errorf("get total cost: %w", err)
	}
	return cost, nil
}

// GetSessionTokenBreakdown returns aggregate token usage and cost for a
// session across all api_calls rows.
func (s *Store) GetSessionTokenBreakdown(sessionID string) (TokenBreakdown, error) {
	var tb TokenBreakdown
	row := s.db.QueryRow(
		`SELECT
		   COALESCE(SUM(input_tokens), 0),
		   COALESCE(SUM(output_tokens), 0),
		   COALESCE(SUM(cache_read_tokens), 0),
		   COALESCE(SUM(cache_write_tokens), 0),
		   COALESCE(SUM(cost), 0),
		   COUNT(*)
		 FROM api_calls WHERE session_id = ?`, sessionID)
	if err := row.Scan(
		&tb.InputTokens, &tb.OutputTokens,
		&tb.CacheReadTokens, &tb.CacheWriteTokens,
		&tb.TotalCost, &tb.CallCount); err != nil {
		return tb, fmt.Errorf("get session token breakdown: %w", err)
	}
	return tb, nil
}
