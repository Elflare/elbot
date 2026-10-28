// 职责：提供 Fork 与压缩结果创建的公开入口。
// 请求和结果在根部转换，材料准备与提交委托 branch 组件执行。
package session

import (
	"context"

	"elbot/internal/session/branch"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Supervisor) Fork(ctx context.Context, scope Scope, fromMessageID string) (*storage.Session, error) {
	return s.branches.Fork(ctx, share.Scope(scope), fromMessageID)
}

func (s *Supervisor) CreateCompacted(ctx context.Context, scope Scope, sourceID string, req CompactedRequest) (*storage.Session, error) {
	return s.branches.CreateCompacted(ctx, share.Scope(scope), sourceID, branch.CompactedRequest{
		ID: req.ID, Title: req.Title, Metadata: req.Metadata,
		Seed: req.Seed, ExpectedCheckpointID: req.ExpectedCheckpointID,
	})
}

// DisplayMaterial 由无原生材料的协议路线显式选择，不在 Session 内判断协议。
type DisplayMaterial struct{}

var _ MaterialPreparer = DisplayMaterial{}

func (DisplayMaterial) PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error) {
	return nil, nil
}

func (DisplayMaterial) PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error) {
	return nil, nil
}
