// 职责：实现会话列表、状态和目标会话查询。
// 按调用方 Scope 判断会话访问范围。
package control

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) ListPage(ctx context.Context, scope share.Scope, query string, limit, offset int, archivedOnly bool) ([]storage.SessionSummary, error) {
	return s.deps.Sessions.List(ctx, storage.ListSessionsRequest{
		ActorID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		IncludeAllPlatforms: scope.IsCLI, IncludeSamePlatformBackground: !scope.IsCLI && scope.ConversationKind == contextinfo.ConversationPrivate,
		ArchivedOnly: archivedOnly, Query: query, Limit: limit, Offset: offset, MessageLimit: 5,
	})
}

func (s *Service) ListResumablePage(ctx context.Context, scope share.Scope, limit, offset int) ([]storage.SessionSummary, error) {
	currentID := ""
	if binding := s.CurrentBinding(scope); binding != nil {
		currentID = binding.SessionID()
	}
	return s.deps.Sessions.List(ctx, storage.ListSessionsRequest{
		ActorID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		IncludeAllPlatforms: scope.IsCLI, IncludeSamePlatformBackground: !scope.IsCLI && scope.ConversationKind == contextinfo.ConversationPrivate,
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
	messages, err := s.deps.Messages.ListBySession(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	status := &Status{Session: row, MessageCount: len(messages)}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		switch message.Role {
		case storage.RoleUser:
			if status.LastUserContent == "" {
				status.LastUserContent = message.Content
			}
		case storage.RoleAssistant:
			if status.LastAnswerContent == "" {
				status.LastAnswerContent = message.Content
			}
		}
		if status.LastUserContent != "" && status.LastAnswerContent != "" {
			break
		}
	}
	return status, nil
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
			return nil, "", nil, storage.ErrNotFound
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
		return fmt.Errorf("session %s is not in current platform scope", row.ID)
	}
	return nil
}
