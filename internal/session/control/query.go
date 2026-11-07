// 职责：实现会话列表、状态和目标会话查询。
// 按调用方 Scope 判断会话访问范围。
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) ListPage(ctx context.Context, scope share.Scope, query string, limit, offset int, archivedOnly bool) ([]storage.SessionSummary, error) {
	return s.deps.Sessions.List(ctx, storage.ListSessionsRequest{
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		IncludeAllPlatforms: scope.IsCLI, IncludeSamePlatformBackground: !scope.IsCLI && scope.ChatType == contextinfo.ChatPrivate,
		ArchivedOnly: archivedOnly, Query: query, Limit: limit, Offset: offset, MessageLimit: 5,
	})
}

func (s *Service) ListResumablePage(ctx context.Context, scope share.Scope, limit, offset int) ([]storage.SessionSummary, error) {
	currentID := ""
	if binding := s.CurrentBinding(scope); binding != nil {
		currentID = binding.SessionID()
	}
	return s.deps.Sessions.List(ctx, storage.ListSessionsRequest{
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		IncludeAllPlatforms: scope.IsCLI, IncludeSamePlatformBackground: !scope.IsCLI && scope.ChatType == contextinfo.ChatPrivate,
		ExcludeSessionID: currentID, Limit: limit, Offset: offset, MessageLimit: 5,
	})
}

func (s *Service) Status(ctx context.Context, scope share.Scope) (*Status, error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	row, err := s.Current(ctx, scope)
	if err != nil {
		return nil, err
	}
	stats, err := s.deps.Messages.Stats(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &Status{Session: row, MessageCount: stats.Count, LastUserContent: stats.LastUserContent, LastAnswerContent: stats.LastAssistantContent}, nil
}

// enterTarget 先解析 Scope 内的空目标，再一次取得 current 和目标 Session 准入。
// 仅解析 ID，不先读行／检查权限；写操作在 Mutate 的最新快照上检查一次。
func (s *Service) enterTarget(ctx context.Context, scope share.Scope, id string) (context.Context, string, func(), error) {
	ctx, leaveScope, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return nil, "", nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		binding := s.CurrentBinding(scope)
		if binding == nil {
			leaveScope()
			return nil, "", nil, ErrNoCurrentSession
		}
		id = binding.SessionID()
	}
	ctx, leaveSessions, err := s.EnterActivation(ctx, scope, id)
	if err != nil {
		leaveScope()
		return nil, "", nil, err
	}
	return ctx, id, func() { leaveSessions(); leaveScope() }, nil
}

func checkAccess(scope share.Scope, row *storage.Session) error {
	if !share.CanAccess(scope, row) {
		return fmt.Errorf("%w: session %s is not in current platform scope", share.ErrAccessDenied, row.ID)
	}
	return nil
}

// ResolveReference 决定引用助手消息后的续接方式；准入时仍复核访问权限。
func (s *Service) ResolveReference(ctx context.Context, scope share.Scope, message storage.Message) (resumeID, forkID string, omitReference bool, err error) {
	row, err := s.deps.Sessions.Get(ctx, message.SessionID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if share.IsBackground(row) {
		if s.deps.IsSuperadmin(scope) {
			return row.ID, "", false, nil
		}
		return "", "", false, nil
	}
	if row.Owner != scope.Owner || row.Platform != scope.Platform || row.ChatType != scope.ChatType || row.ChatID != scope.ChatID {
		return "", "", false, nil
	}
	messages, err := s.deps.Messages.ListBySession(ctx, row.ID)
	if err != nil {
		return "", "", false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			if messages[i].ID == message.ID {
				return row.ID, "", true, nil
			}
			break
		}
	}
	return "", message.ID, false, nil
}
