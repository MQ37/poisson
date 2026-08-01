package store

import (
	"database/sql"
	"fmt"
)

// migrateUniqueSeq (v1 -> v2) backs the seq TOCTOU fixes (messages.seq: see
// nextSeqTx; api_calls.seq: see nextAPICallSeqTx) with a real DB constraint
// instead of relying purely on application-level transaction discipline — a
// future regression then fails loudly (constraint violation) instead of
// silently corrupting the ORDER BY seq ordering every reader relies on.
//
// Existing (session_id, seq) duplicates, if any slipped in before either fix
// landed, are renumbered first — never deleted — so the unique index can be
// created without losing history.
func migrateUniqueSeq(db *sql.DB) error {
	if err := dedupeSeq(db, "messages"); err != nil {
		return err
	}
	if err := dedupeSeq(db, "api_calls"); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq_uniq ON messages(session_id, seq)`); err != nil {
		return fmt.Errorf("create unique index on messages: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_calls_session_seq_uniq ON api_calls(session_id, seq)`); err != nil {
		return fmt.Errorf("create unique index on api_calls: %w", err)
	}
	return nil
}

// dedupeSeq renumbers any (session_id, seq) duplicates in table so a unique
// index can be created over it. Within each duplicate group, the row with
// the smallest rowid (earliest inserted) keeps its seq; every later row is
// reassigned the session's next free seq — rows are never deleted.
func dedupeSeq(db *sql.DB, table string) error {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT rowid, session_id, seq FROM %s
		WHERE (session_id, seq) IN (
			SELECT session_id, seq FROM %s GROUP BY session_id, seq HAVING COUNT(*) > 1
		)
		ORDER BY session_id, rowid`, table, table))
	if err != nil {
		return fmt.Errorf("find duplicate seq in %s: %w", table, err)
	}
	type dupRow struct {
		rowid     int64
		sessionID string
		seq       int
	}
	var dups []dupRow
	for rows.Next() {
		var r dupRow
		if err := rows.Scan(&r.rowid, &r.sessionID, &r.seq); err != nil {
			rows.Close()
			return fmt.Errorf("scan duplicate seq in %s: %w", table, err)
		}
		dups = append(dups, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if len(dups) == 0 {
		return nil
	}

	nextFree := map[string]int{}    // session_id -> next unused seq, lazily seeded from MAX(seq)
	keptOnce := map[string]bool{}   // "session_id:seq" whose first occurrence already kept its value
	for _, r := range dups {
		key := fmt.Sprintf("%s:%d", r.sessionID, r.seq)
		if !keptOnce[key] {
			keptOnce[key] = true
			continue
		}
		if _, ok := nextFree[r.sessionID]; !ok {
			var maxSeq int
			if err := db.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(seq), 0) FROM %s WHERE session_id = ?`, table), r.sessionID).Scan(&maxSeq); err != nil {
				return fmt.Errorf("find max seq in %s: %w", table, err)
			}
			nextFree[r.sessionID] = maxSeq + 1
		}
		newSeq := nextFree[r.sessionID]
		nextFree[r.sessionID]++
		if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET seq = ? WHERE rowid = ?`, table), newSeq, r.rowid); err != nil {
			return fmt.Errorf("renumber duplicate seq in %s: %w", table, err)
		}
	}
	return nil
}
