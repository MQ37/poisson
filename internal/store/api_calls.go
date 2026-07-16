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
	ID                 string
	SessionID          string
	Seq                int
	Provider           string
	Model              string
	InputTokens        int
	InputTokensUnknown bool
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	Cost               float64
	IsCompaction       bool
	CreatedAt          int64
}

// TotalInputTokens is the full prompt size the model processed: uncached input
// plus cache reads and cache writes. Providers that cache (e.g. Anthropic)
// report input_tokens EXCLUDING cached tokens, so InputTokens alone undercounts
// the active context.
func (c *APICall) TotalInputTokens() int {
	return c.InputTokens + c.CacheReadTokens + c.CacheWriteTokens
}

// TotalContextTokens is the full size of this turn in the conversation: the
// prompt (uncached input + cache reads + cache writes) plus the assistant's
// output, which becomes part of the next request's context. Mirrors pi-mono's
// calculateContextTokens and is what the context-usage indicator should track.
func (c *APICall) TotalContextTokens() int {
	return c.InputTokens + c.OutputTokens + c.CacheReadTokens + c.CacheWriteTokens
}

// TokenBreakdown is the aggregate token usage for a session.
type TokenBreakdown struct {
	InputTokens       int
	InputUnknownCalls int
	OutputTokens      int
	CacheReadTokens   int
	CacheWriteTokens  int
	TotalCost         float64
	CallCount         int
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
	inputKnown := 1
	if call.InputTokensUnknown {
		inputKnown = 0
	}
	isCompaction := 0
	if call.IsCompaction {
		isCompaction = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO api_calls
		 (id, session_id, seq, provider, model, input_tokens, input_tokens_known, output_tokens,
		  cache_read_tokens, cache_write_tokens, cost, is_compaction, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		call.ID, call.SessionID, call.Seq, call.Provider, call.Model,
		call.InputTokens, inputKnown, call.OutputTokens,
		call.CacheReadTokens, call.CacheWriteTokens,
		call.Cost, isCompaction, call.CreatedAt)
	if err != nil {
		return fmt.Errorf("record api call: %w", err)
	}
	return nil
}

// GetLastAPICall returns the most recent non-compaction api_calls row for a
// session (by created_at desc), or ErrNotFound. Compaction summarization rows
// are excluded — they must not drive context % or auto-compact triggers.
func (s *Store) GetLastAPICall(sessionID string) (*APICall, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, seq, provider, model, input_tokens, input_tokens_known, output_tokens,
		        cache_read_tokens, cache_write_tokens, cost, is_compaction, created_at
		 FROM api_calls WHERE session_id = ? AND is_compaction = 0
		 ORDER BY created_at DESC, seq DESC LIMIT 1`, sessionID)
	var c APICall
	var inputKnown, isCompaction int
	err := row.Scan(
		&c.ID, &c.SessionID, &c.Seq, &c.Provider, &c.Model,
		&c.InputTokens, &inputKnown, &c.OutputTokens,
		&c.CacheReadTokens, &c.CacheWriteTokens,
		&c.Cost, &isCompaction, &c.CreatedAt)
	c.InputTokensUnknown = inputKnown == 0
	c.IsCompaction = isCompaction != 0
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
		   COALESCE(SUM(CASE WHEN input_tokens_known = 1 THEN input_tokens ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN input_tokens_known = 0 THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(output_tokens), 0),
		   COALESCE(SUM(cache_read_tokens), 0),
		   COALESCE(SUM(cache_write_tokens), 0),
		   COALESCE(SUM(cost), 0),
		   COUNT(*)
		 FROM api_calls WHERE session_id = ?`, sessionID)
	if err := row.Scan(
		&tb.InputTokens, &tb.InputUnknownCalls, &tb.OutputTokens,
		&tb.CacheReadTokens, &tb.CacheWriteTokens,
		&tb.TotalCost, &tb.CallCount); err != nil {
		return tb, fmt.Errorf("get session token breakdown: %w", err)
	}
	return tb, nil
}
