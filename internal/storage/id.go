package storage

import (
	"context"
	"crypto/rand"
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
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("generate id: %v", err))
	}

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
