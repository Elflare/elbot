package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// reserveSequence commits the range independently of business transactions.
func reserveSequence(ctx context.Context, db *sql.DB, name string, count int64) (int64, int64, error) {
	var end, size int64
	err := db.QueryRowContext(ctx, `UPDATE id_sequences
 SET reserved = min(?, 9223372036854775807 - value),
 value = value + min(?, 9223372036854775807 - value)
 WHERE name = ? AND value < 9223372036854775807
 RETURNING value, reserved`, count, count, name).Scan(&end, &size)
	if err != nil {
		return 0, 0, fmt.Errorf("reserve %s IDs: %w", name, err)
	}
	return end - size + 1, end, nil
}

// NextIDSequence allocates from the shared non-Session range. Call before a business transaction.
func (s *Store) NextIDSequence(ctx context.Context) (int64, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.idNext == 0 {
		start, end, err := reserveSequence(ctx, s.db, "general", 1000)
		if err != nil {
			return 0, err
		}
		s.idNext, s.idEnd = start, end
	}
	value := s.idNext
	if value == s.idEnd {
		s.idNext = 0
	} else {
		s.idNext++
	}
	return value, nil
}
