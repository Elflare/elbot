// 职责：提供后台会话准备、复制及前台接管事实查询的公开入口。
// 准备和查询委托 control，副本创建委托 branch。
package session

import (
	"context"

	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Supervisor) PrepareBackground(ctx context.Context, scope Scope, req BackgroundRequest) (*storage.Session, error) {
	return s.control.PrepareBackground(ctx, share.Scope(scope), control.BackgroundRequest{
		SessionID: req.SessionID, Kind: req.Kind, Name: req.Name, Title: req.Title, Metadata: req.Metadata,
	})
}

func (s *Supervisor) CopyBackground(ctx context.Context, target Scope, req BackgroundCopyRequest) (*storage.Session, error) {
	return s.branches.CopyBackground(ctx, share.Scope(target), branch.BackgroundCopyRequest{
		SourceSessionID: req.SourceSessionID, Kind: req.Kind, Name: req.Name,
		Title: req.Title, Metadata: req.Metadata,
	})
}

func (s *Supervisor) SessionTakenOver(ctx context.Context, id string) (bool, error) {
	return s.control.SessionTakenOver(ctx, id)
}

// IsBackground 只读取已保存的会话事实，不查询当前执行状态。
func IsBackground(row *storage.Session) bool { return share.IsBackground(row) }

func WasPromoted(row *storage.Session) bool { return share.WasPromoted(row) }
