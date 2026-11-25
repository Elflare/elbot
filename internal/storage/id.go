package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strconv"
)

// IDSequence 借用持久化序号能力；前缀由身份所属业务确定。
type IDSequence func(context.Context) (int64, error)

func (next IDSequence) ID(ctx context.Context, prefix string) (string, error) {
	n, err := next(ctx)
	if err != nil {
		return "", err
	}
	return prefix + strconv.FormatInt(n, 10), nil
}

// MessageID 是消息身份的唯一格式化与发号入口，先于引用构造和事务提交。
func (next IDSequence) MessageID(ctx context.Context) (string, error) { return next.ID(ctx, "m") }

// PrepareMessageID 在引用构造前分配一次；已持有身份的消息更新保留原 ID。
func PrepareMessageID(ctx context.Context, allocate func(context.Context) (string, error), message *Message) error {
	if message.ID != "" {
		return nil
	}
	id, err := allocate(ctx)
	if err != nil {
		return err
	}
	message.ID = id
	return nil
}

func NewID() string {
	var b [16]byte
	rand.Read(b[:])

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

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
func (s *Supervisor) NextIDSequence(ctx context.Context) (int64, error) {
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
