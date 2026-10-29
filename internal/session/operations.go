// 职责：提供前台会话创建、恢复、查询、管理及过期处理的公开入口。
// 实际业务委托 control 组件执行。
package session

import (
	"context"
	"time"

	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Supervisor) GetOrCreateCurrent(ctx context.Context, scope Scope, firstMessage string) (*storage.Session, error) {
	return s.control.GetOrCreateCurrent(ctx, share.Scope(scope), firstMessage)
}

func (s *Supervisor) Create(ctx context.Context, scope Scope, req CreateRequest) (*storage.Session, error) {
	return s.control.Create(ctx, share.Scope(scope), control.CreateRequest{
		ID: req.ID, Title: req.Title, Mode: req.Mode, Metadata: req.Metadata,
	})
}

func (s *Supervisor) DefaultMode() string { return s.control.DefaultMode() }

func (s *Supervisor) Current(ctx context.Context, scope Scope) (*storage.Session, error) {
	return s.control.Current(ctx, share.Scope(scope))
}

func (s *Supervisor) Resume(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.control.Resume(ctx, share.Scope(scope), sessionID)
}

// ResetCurrent 只解除 current 绑定，下一条输入再创建会话。
func (s *Supervisor) ResetCurrent(ctx context.Context, scope Scope) error {
	return s.control.ResetCurrent(ctx, share.Scope(scope))
}

func (s *Supervisor) ActivateMode(ctx context.Context, scope Scope, req ActivateModeRequest) (ActivateModeResult, error) {
	result, err := s.control.ActivateMode(ctx, share.Scope(scope), control.ActivateModeRequest{
		Mode: req.Mode, NewSessionTitle: req.NewSessionTitle,
	})
	return ActivateModeResult{Session: result.Session, AlreadyActive: result.AlreadyActive}, err
}

func (s *Supervisor) List(ctx context.Context, scope Scope, query string, limit int) ([]storage.SessionSummary, error) {
	return s.control.ListPage(ctx, share.Scope(scope), query, limit, 0, false)
}

func (s *Supervisor) ListPage(ctx context.Context, scope Scope, query string, limit, offset int, archivedOnly bool) ([]storage.SessionSummary, error) {
	return s.control.ListPage(ctx, share.Scope(scope), query, limit, offset, archivedOnly)
}

func (s *Supervisor) ListResumablePage(ctx context.Context, scope Scope, limit, offset int) ([]storage.SessionSummary, error) {
	return s.control.ListResumablePage(ctx, share.Scope(scope), limit, offset)
}

func (s *Supervisor) Status(ctx context.Context, scope Scope) (*Status, error) {
	result, err := s.control.Status(ctx, share.Scope(scope))
	if result == nil {
		return nil, err
	}
	return &Status{
		Session: result.Session, MessageCount: result.MessageCount,
		LastUserContent: result.LastUserContent, LastAnswerContent: result.LastAnswerContent,
	}, err
}

func (s *Supervisor) Rename(ctx context.Context, scope Scope, sessionID, title string) (*storage.Session, error) {
	return s.control.Rename(ctx, share.Scope(scope), sessionID, title)
}

func (s *Supervisor) Archive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.control.Archive(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Unarchive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.control.Unarchive(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Pin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.control.Pin(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Unpin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.control.Unpin(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Delete(ctx context.Context, scope Scope, sessionID string) error {
	return s.control.Delete(ctx, share.Scope(scope), sessionID)
}

// CleanupExpired 返回实际删除数量，包括后续出错前已完成的部分。
func (s *Supervisor) CleanupExpired(ctx context.Context, cutoff time.Time) (int, error) {
	return s.control.CleanupExpired(ctx, cutoff)
}

// RecordActivity 由有效输入入口调用，旧激活不能为新绑定续期。
func (s *Supervisor) RecordActivity(ctx context.Context, binding *Binding) error {
	return s.control.RecordActivity(ctx, (*share.Binding)(binding))
}

// SessionIdle 由实际执行收尾在业务准入外调用；失败和取消也须在真实退出后上报。
// 取消执行的收尾使用独立的清理 context，避免把已完成通知随请求一起取消。
func (s *Supervisor) SessionIdle(ctx context.Context, sessionID string) error {
	return s.control.SessionIdle(ctx, sessionID)
}
