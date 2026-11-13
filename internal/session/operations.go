// 职责：提供前台会话创建、恢复、查询、管理及过期处理的公开入口。
// 实际业务委托 control 组件执行。
package session

import (
	"context"
	"time"

	"elbot/internal/operationresult"
	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Supervisor) GetOrCreateCurrent(ctx context.Context, scope Scope, firstMessage string) (operationresult.Result[*storage.Session], error) {
	return s.control.GetOrCreateCurrent(ctx, share.Scope(scope), firstMessage)
}

func (s *Supervisor) Create(ctx context.Context, scope Scope, req CreateRequest) (operationresult.Result[*storage.Session], error) {
	return s.control.Create(ctx, share.Scope(scope), control.CreateRequest{
		Title: req.Title, Mode: req.Mode, Metadata: req.Metadata,
	})
}

func (s *Supervisor) DefaultMode() string { return s.control.DefaultMode() }

func (s *Supervisor) Current(ctx context.Context, scope Scope) (operationresult.Result[*storage.Session], error) {
	return s.control.Current(ctx, share.Scope(scope))
}

func (s *Supervisor) Resume(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[*storage.Session], error) {
	if scope.IsSuperadmin {
		copied, copiedSession, err := s.branches.CopyForeground(ctx, share.Scope(scope), sessionID)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		if copied.Status != operationresult.Success {
			return copied, nil
		}
		if copiedSession {
			return copied, nil
		}
	}
	return s.control.Resume(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) ResumeLast(ctx context.Context, scope Scope, allPlatforms bool) (operationresult.Result[*storage.Session], error) {
	found, err := s.control.FindLast(ctx, share.Scope(scope), allPlatforms)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if found.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: found.Status, Message: found.Message}, nil
	}
	return s.Resume(ctx, scope, found.Data)
}

// ResetCurrent 只解除 current 绑定，下一条输入再创建会话。
func (s *Supervisor) ResetCurrent(ctx context.Context, scope Scope) (operationresult.Result[struct{}], error) {
	return s.control.ResetCurrent(ctx, share.Scope(scope))
}

func (s *Supervisor) ActivateMode(ctx context.Context, scope Scope, req ActivateModeRequest) (operationresult.Result[ActivateModeResult], error) {
	result, err := s.control.ActivateMode(ctx, share.Scope(scope), control.ActivateModeRequest{
		Mode: req.Mode, NewSessionTitle: req.NewSessionTitle,
	})
	if err != nil {
		return operationresult.Result[ActivateModeResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[ActivateModeResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[ActivateModeResult]{Status: result.Status, Message: result.Message, Data: ActivateModeResult{Session: result.Data.Session, AlreadyActive: result.Data.AlreadyActive}}, nil
}

func (s *Supervisor) List(ctx context.Context, scope Scope, query string, limit int) (operationresult.Result[[]storage.SessionSummary], error) {
	result, err := s.ListPage(ctx, scope, ListRequest{Query: query, Page: 1, PageSize: limit})
	if err != nil {
		return operationresult.Result[[]storage.SessionSummary]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[[]storage.SessionSummary]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[[]storage.SessionSummary]{Status: result.Status, Message: result.Message, Data: result.Data.Items}, nil
}

func (s *Supervisor) ListPage(ctx context.Context, scope Scope, req ListRequest) (operationresult.Result[ListResult], error) {
	result, err := s.control.ListPage(ctx, share.Scope(scope), control.ListRequest(req))
	if err != nil {
		return operationresult.Result[ListResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[ListResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[ListResult]{Status: result.Status, Message: result.Message, Data: ListResult(result.Data)}, nil
}

func (s *Supervisor) Status(ctx context.Context, scope Scope) (operationresult.Result[*Status], error) {
	result, err := s.control.Status(ctx, share.Scope(scope))
	if err != nil {
		return operationresult.Result[*Status]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[*Status]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[*Status]{Status: result.Status, Message: result.Message, Data: &Status{
		Session: result.Data.Session, MessageCount: result.Data.MessageCount,
		LastUserContent: result.Data.LastUserContent, LastAnswerContent: result.Data.LastAnswerContent,
	}}, nil
}

func (s *Supervisor) Rename(ctx context.Context, scope Scope, sessionID, title string) (operationresult.Result[*storage.Session], error) {
	return s.control.Rename(ctx, share.Scope(scope), sessionID, title)
}

func (s *Supervisor) Archive(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[*storage.Session], error) {
	return s.control.Archive(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Unarchive(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[*storage.Session], error) {
	return s.control.Unarchive(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Pin(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[*storage.Session], error) {
	return s.control.Pin(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Unpin(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[*storage.Session], error) {
	return s.control.Unpin(ctx, share.Scope(scope), sessionID)
}

func (s *Supervisor) Delete(ctx context.Context, scope Scope, sessionID string) (operationresult.Result[struct{}], error) {
	return s.control.Delete(ctx, share.Scope(scope), sessionID)
}

// CleanupExpired 是内部维护扫描，返回实际删除数量和故障。
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

// ResolveReference 提供引用消息的续接判定，不暴露 Session 模式给 Platform。
func (s *Supervisor) ResolveReference(ctx context.Context, scope Scope, message storage.Message) (resumeID, forkID string, omitReference bool, err error) {
	return s.control.ResolveReference(ctx, share.Scope(scope), message)
}
