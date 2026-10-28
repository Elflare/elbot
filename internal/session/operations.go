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

func (s *Supervisor) ExpireIdleCurrent(ctx context.Context, req ExpireIdleRequest) (ExpireIdleResult, error) {
	result, err := s.control.ExpireIdleCurrent(ctx, control.ExpireIdleRequest{
		Scope: share.Scope(req.Scope), IsSuperadmin: req.IsSuperadmin,
		Config: control.IdleExpirationConfig(req.Config), Now: req.Now,
	})
	return ExpireIdleResult{Expired: result.Expired, SessionID: result.SessionID, TTLMinutes: result.TTLMinutes}, err
}
