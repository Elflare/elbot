// 职责：提供会话模型协议归属的公开能力。
// 归属登记委托 control，解析与继承复用 share 中的规则。
package session

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// Origin 只解析会话保存的协议归属，不用当前模型配置重新解释历史。
func Origin(row *storage.Session) (llm.Origin, bool, error) { return share.Origin(row) }

func (s *Supervisor) RegisterOrigin(ctx context.Context, id string, target llm.Origin) (*storage.Session, error) {
	return s.control.RegisterOrigin(ctx, id, target)
}
