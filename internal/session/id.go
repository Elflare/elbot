// 职责：统一分配 Session ID；存储只负责持久化全局序号。
package session

import (
	"context"
	"strconv"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// AllocateID 为源会话的压缩派生预分配 ID，保留其创建来源事实。
// 发号先于业务提交，取消或提交失败时允许跳号，号码不回收。
func (s *Supervisor) AllocateID(ctx context.Context, scope Scope, source *storage.Session) (string, error) {
	origin, err := share.SessionIDOrigin(source, share.Scope(scope))
	if err != nil {
		return "", err
	}
	return s.allocateID(ctx, origin)
}

func (s *Supervisor) allocateID(ctx context.Context, origin share.IDOrigin) (string, error) {
	prefix, err := origin.Prefix()
	if err != nil {
		return "", err
	}
	sequence, err := s.nextSequence(ctx)
	if err != nil {
		return "", err
	}
	return prefix + strconv.FormatInt(sequence, 10), nil
}
