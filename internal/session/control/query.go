// 职责：实现会话列表、状态和目标会话查询。
// 按调用方 Scope 判断会话访问范围。
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) ListPage(ctx context.Context, scope share.Scope, req ListRequest) (operationresult.Result[ListResult], error) {
	result := ListResult{Items: []storage.SessionSummary{}, Page: req.Page, PageSize: req.PageSize}
	if req.Page < 0 || req.PageSize <= 0 {
		return operationresult.Result[ListResult]{Status: operationresult.InvalidArguments, Message: "invalid session pagination"}, nil
	}
	query := storage.ListSessionsRequest{
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		IncludeAllPlatforms: req.AllPlatforms, IncludeSamePlatformBackground: !req.AllPlatforms && scope.ChatType == contextinfo.ChatPrivate,
		ArchivedOnly: req.ArchivedOnly, Query: req.Query, Limit: req.PageSize, MessageLimit: 5,
	}
	if req.ExcludeCurrent {
		if binding := s.CurrentBinding(scope); binding != nil {
			query.ExcludeSessionID = binding.SessionID()
		}
	}
	var err error
	result.TotalCount, err = s.deps.Sessions.Count(ctx, query)
	if err != nil {
		return operationresult.Result[ListResult]{}, err
	}
	result.TotalPages = result.TotalCount / req.PageSize
	if result.TotalCount%req.PageSize != 0 {
		result.TotalPages++
	}
	if result.Page == 0 {
		result.Page = 1
	}
	if result.TotalCount == 0 {
		if req.Page > 1 {
			return operationresult.Result[ListResult]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid session page: %d", req.Page)}, nil
		}
		return operationresult.Result[ListResult]{Status: operationresult.Success, Data: result}, nil
	}
	if result.Page > result.TotalPages {
		return operationresult.Result[ListResult]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid session page: %d", result.Page)}, nil
	}
	query.Offset = (result.Page - 1) * req.PageSize
	result.Items, err = s.deps.Sessions.List(ctx, query)
	if err != nil {
		return operationresult.Result[ListResult]{}, err
	}
	return operationresult.Result[ListResult]{Status: operationresult.Success, Data: result}, nil
}

func (s *Service) Status(ctx context.Context, scope share.Scope) (operationresult.Result[*Status], error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return operationresult.Result[*Status]{}, err
	}
	defer release()
	current, err := s.Current(ctx, scope)
	if err != nil {
		return operationresult.Result[*Status]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[*Status]{Status: current.Status, Message: current.Message}, nil
	}
	row := current.Data
	stats, err := s.deps.Messages.Stats(ctx, row.ID)
	if err != nil {
		return operationresult.Result[*Status]{}, err
	}
	return operationresult.Result[*Status]{Status: operationresult.Success, Data: &Status{Session: row, MessageCount: stats.Count, LastUserContent: stats.LastUserContent, LastAnswerContent: stats.LastAssistantContent}}, nil
}

// enterTarget 先解析 Scope 内的空目标，再一次取得 current 和目标 Session 准入。
// 仅解析 ID，不先读行／检查权限；写操作在 Mutate 的最新快照上检查一次。
type EnterTargetData struct {
	Context context.Context
	ID      string
	Release func()
}

func (s *Service) enterTarget(ctx context.Context, scope share.Scope, id string) (operationresult.Result[EnterTargetData], error) {
	ctx, leaveScope, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return operationresult.Result[EnterTargetData]{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		binding := s.CurrentBinding(scope)
		if binding == nil {
			leaveScope()
			return operationresult.Result[EnterTargetData]{Status: operationresult.NoCurrentSession, Message: "当前没有会话"}, nil
		}
		id = binding.SessionID()
	}
	ctx, leaveSessions, err := s.EnterActivation(ctx, scope, id)
	if err != nil {
		leaveScope()
		return operationresult.Result[EnterTargetData]{}, err
	}
	return operationresult.Result[EnterTargetData]{Status: operationresult.Success, Data: EnterTargetData{Context: ctx, ID: id, Release: func() { leaveSessions(); leaveScope() }}}, nil
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
