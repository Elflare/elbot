package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/control/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) ListSessions(ctx context.Context, req ListSessionsRequest) (operationresult.Result[ListSessionsResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[ListSessionsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListSessionsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	actor := authorized.Data
	if req.AllPlatforms && actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[ListSessionsResult]{Status: operationresult.Forbidden, Message: "仅超级管理员可查看所有平台会话"}, nil
	}
	listed, err := m.deps.Sessions.ListPage(ctx, m.deps.Scope(ctx), session.ListRequest{Query: req.Query, Page: req.Page, PageSize: req.PageSize, ArchivedOnly: req.Archived, ExcludeCurrent: req.ExcludeCurrent, AllPlatforms: req.AllPlatforms})
	if err != nil {
		return operationresult.Result[ListSessionsResult]{}, err
	}
	if listed.Status != operationresult.Success {
		return operationresult.Result[ListSessionsResult]{Status: listed.Status, Message: listed.Message}, nil
	}
	current, err := m.currentID(ctx)
	if err != nil {
		return operationresult.Result[ListSessionsResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[ListSessionsResult]{Status: current.Status, Message: current.Message}, nil
	}
	result := ListSessionsResult{Items: []SessionSummaryData{}, Query: req.Query, CurrentSessionID: current.Data}
	result.Page = Page{Page: listed.Data.Page, PageSize: listed.Data.PageSize, TotalPages: listed.Data.TotalPages, HasNext: listed.Data.Page < listed.Data.TotalPages}
	for _, row := range listed.Data.Items {
		messages, err := messageData(row.Messages)
		if err != nil {
			return operationresult.Result[ListSessionsResult]{}, err
		}
		result.Items = append(result.Items, SessionSummaryData{SessionData: *sessionData(&storage.Session{ID: row.ID, Title: row.Title, Mode: row.Mode, Status: row.Status, Platform: row.Platform, ChatID: row.ChatID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt, PinnedAt: row.PinnedAt, ParentSessionID: row.ParentSessionID, ForkFromMessageID: row.ForkFromMessageID}), MessageCount: row.MessageCount, Messages: messages, LastUserContent: row.LastUserContent, LastAssistantContent: row.LastAssistantContent})
	}
	return operationresult.Result[ListSessionsResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) GetSession(ctx context.Context, req GetSessionRequest) (operationresult.Result[GetSessionResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[GetSessionResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[GetSessionResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[GetSessionResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[GetSessionResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	row, err := m.readSession(ctx, resolved.Data)
	if err != nil {
		return operationresult.Result[GetSessionResult]{}, err
	}
	if row.Status != operationresult.Success {
		return operationresult.Result[GetSessionResult]{Status: row.Status, Message: row.Message}, nil
	}
	return operationresult.Result[GetSessionResult]{Status: operationresult.Success, Data: *sessionData(row.Data)}, nil
}

func (m *Supervisor) NewSession(ctx context.Context) (operationresult.Result[NewSessionResult], error) {
	ready, err := m.beforeSession(ctx)
	if err != nil {
		return operationresult.Result[NewSessionResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[NewSessionResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	ctx = ready.Data
	result, err := m.deps.Sessions.ResetCurrent(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[NewSessionResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[NewSessionResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[NewSessionResult]{Status: operationresult.Success}, nil
}

func (m *Supervisor) RenameSession(ctx context.Context, req RenameSessionRequest) (operationresult.Result[RenameSessionResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[RenameSessionResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[RenameSessionResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	target, title := strings.TrimSpace(req.Target), req.Title
	id := ""
	if target == "" {
		resolved, err := m.resolveSessionID(ctx, "")
		if err != nil {
			return operationresult.Result[RenameSessionResult]{}, err
		}
		if resolved.Status != operationresult.Success {
			return operationresult.Result[RenameSessionResult]{Status: resolved.Status, Message: resolved.Message}, nil
		}
		id = resolved.Data
	} else {
		row, err := m.readSession(ctx, target)
		if err != nil {
			return operationresult.Result[RenameSessionResult]{}, err
		}
		if row.Status == operationresult.Success {
			id = row.Data.ID
		} else if row.Status != operationresult.NotFound {
			return operationresult.Result[RenameSessionResult]{Status: row.Status, Message: row.Message}, nil
		} else {
			if req.PageSize <= 0 {
				return operationresult.Result[RenameSessionResult]{Status: operationresult.InvalidArguments, Message: "page_size 必须为正数"}, nil
			}
			matches := []string{}
			for page := 1; ; page++ {
				listed, err := m.deps.Sessions.ListPage(ctx, m.deps.Scope(ctx), session.ListRequest{Query: target, Page: page, PageSize: req.PageSize})
				if err != nil {
					return operationresult.Result[RenameSessionResult]{}, err
				}
				if listed.Status != operationresult.Success {
					return operationresult.Result[RenameSessionResult]{Status: listed.Status, Message: listed.Message}, nil
				}
				for _, row := range listed.Data.Items {
					if row.Title == target {
						matches = append(matches, row.ID)
					}
				}
				if page >= listed.Data.TotalPages {
					break
				}
			}
			switch len(matches) {
			case 0:
				resolved, err := m.resolveSessionID(ctx, "")
				if err != nil {
					return operationresult.Result[RenameSessionResult]{}, err
				}
				if resolved.Status != operationresult.Success {
					return operationresult.Result[RenameSessionResult]{Status: resolved.Status, Message: resolved.Message}, nil
				}
				id, title = resolved.Data, target+" "+title
			case 1:
				id = matches[0]
			default:
				return operationresult.Result[RenameSessionResult]{Status: operationresult.InvalidArguments, Message: "session title 匹配多个会话，请使用 session ID"}, nil
			}
		}
	}
	renamed, err := m.deps.Sessions.Rename(ctx, m.deps.Scope(ctx), id, title)
	if err != nil {
		return operationresult.Result[RenameSessionResult]{}, err
	}
	if renamed.Status != operationresult.Success {
		return operationresult.Result[RenameSessionResult]{Status: renamed.Status, Message: renamed.Message}, nil
	}
	row := renamed.Data
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) SetSessionArchived(ctx context.Context, req SetSessionArchivedRequest) (operationresult.Result[SetSessionArchivedResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[SetSessionArchivedResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[SetSessionArchivedResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.Archived && !req.Confirmed {
		return operationresult.Result[SetSessionArchivedResult]{Status: operationresult.Rejected, Message: "归档操作需要确认"}, nil
	}
	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[SetSessionArchivedResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[SetSessionArchivedResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	var changed operationresult.Result[*storage.Session]
	if req.Archived {
		changed, err = m.deps.Sessions.Archive(ctx, m.deps.Scope(ctx), resolved.Data)
	} else {
		changed, err = m.deps.Sessions.Unarchive(ctx, m.deps.Scope(ctx), resolved.Data)
	}
	if err != nil {
		return operationresult.Result[SetSessionArchivedResult]{}, err
	}
	if changed.Status != operationresult.Success {
		return operationresult.Result[SetSessionArchivedResult]{Status: changed.Status, Message: changed.Message}, nil
	}
	row := changed.Data
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) SetSessionPinned(ctx context.Context, req SetSessionPinnedRequest) (operationresult.Result[SetSessionPinnedResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[SetSessionPinnedResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[SetSessionPinnedResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}

	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[SetSessionPinnedResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[SetSessionPinnedResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	var changed operationresult.Result[*storage.Session]
	if req.Pinned {
		changed, err = m.deps.Sessions.Pin(ctx, m.deps.Scope(ctx), resolved.Data)
	} else {
		changed, err = m.deps.Sessions.Unpin(ctx, m.deps.Scope(ctx), resolved.Data)
	}
	if err != nil {
		return operationresult.Result[SetSessionPinnedResult]{}, err
	}
	if changed.Status != operationresult.Success {
		return operationresult.Result[SetSessionPinnedResult]{Status: changed.Status, Message: changed.Message}, nil
	}
	row := changed.Data
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) DeleteSession(ctx context.Context, req DeleteSessionRequest) (operationresult.Result[DeleteSessionResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[DeleteSessionResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[DeleteSessionResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return operationresult.Result[DeleteSessionResult]{Status: operationresult.InvalidArguments, Message: "session_id 不能为空"}, nil
	}
	if !req.Confirmed {
		return operationresult.Result[DeleteSessionResult]{Status: operationresult.Rejected, Message: "删除操作需要确认"}, nil
	}
	deleted, err := m.deps.Sessions.Delete(ctx, m.deps.Scope(ctx), req.SessionID)
	if err != nil {
		return operationresult.Result[DeleteSessionResult]{}, err
	}
	if deleted.Status != operationresult.Success {
		return operationresult.Result[DeleteSessionResult]{Status: deleted.Status, Message: deleted.Message}, nil
	}
	return m.sessionChange(ctx, nil, req.SessionID, 1)
}

func (m *Supervisor) CleanSessions(ctx context.Context, req CleanSessionsRequest) (operationresult.Result[CleanSessionsResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[CleanSessionsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[CleanSessionsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if !req.Confirmed {
		return operationresult.Result[CleanSessionsResult]{Status: operationresult.Rejected, Message: "清理操作需要确认"}, nil
	}
	cleaned, err := m.deps.Sessions.CleanupExpired(ctx, req.Before)
	if err != nil {
		return operationresult.Result[CleanSessionsResult]{}, err
	}
	return operationresult.Result[CleanSessionsResult]{Status: operationresult.Success, Data: CleanSessionsResult{AffectedCount: cleaned}}, nil
}

func (m *Supervisor) ResumeSession(ctx context.Context, req ResumeSessionRequest) (operationresult.Result[ResumeSessionResult], error) {
	ready, err := m.beforeSession(ctx)
	if err != nil {
		return operationresult.Result[ResumeSessionResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[ResumeSessionResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	ctx = ready.Data
	if req.MessageLimit < 0 {
		return operationresult.Result[ResumeSessionResult]{Status: operationresult.InvalidArguments, Message: "message_limit 不能为负数"}, nil
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return operationresult.Result[ResumeSessionResult]{Status: operationresult.InvalidArguments, Message: "session_id 不能为空"}, nil
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	if req.AllPlatforms && (actor.Role != contextinfo.RoleSuperadmin || req.SessionID != "last") {
		return operationresult.Result[ResumeSessionResult]{Status: operationresult.Forbidden, Message: "仅超级管理员可对 last 使用 -a/--all"}, nil
	}
	var resumed operationresult.Result[*storage.Session]
	if req.SessionID == "last" {
		resumed, err = m.deps.Sessions.ResumeLast(ctx, m.deps.Scope(ctx), req.AllPlatforms)
	} else {
		resumed, err = m.deps.Sessions.Resume(ctx, m.deps.Scope(ctx), req.SessionID)
	}
	if err != nil {
		return operationresult.Result[ResumeSessionResult]{}, err
	}
	if resumed.Status != operationresult.Success {
		return operationresult.Result[ResumeSessionResult]{Status: resumed.Status, Message: resumed.Message}, nil
	}
	row := resumed.Data
	record := events.LogRecord{Category: events.LogCategoryAudit, Level: slog.LevelInfo, EventName: "session_resume", Module: "control", Content: "title: " + row.Title, ResultStatus: events.ResultSucceeded, Identity: events.LogIdentityFromContext(ctx)}
	record.Identity.SessionID = row.ID
	events.Log(ctx, record)
	return m.sessionPreview(ctx, row, req.MessageLimit)
}

func (m *Supervisor) ForkSession(ctx context.Context, req ForkSessionRequest) (operationresult.Result[ForkSessionResult], error) {
	ready, err := m.beforeSession(ctx)
	if err != nil {
		return operationresult.Result[ForkSessionResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[ForkSessionResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	ctx = ready.Data
	if req.MessageLimit < 0 {
		return operationresult.Result[ForkSessionResult]{Status: operationresult.InvalidArguments, Message: "message_limit 不能为负数"}, nil
	}
	if strings.TrimSpace(req.MessageID) == "" {
		return operationresult.Result[ForkSessionResult]{Status: operationresult.InvalidArguments, Message: "message_id 不能为空"}, nil
	}
	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[ForkSessionResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[ForkSessionResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	current, err := m.requireCurrent(ctx, resolved.Data)
	if err != nil {
		return operationresult.Result[ForkSessionResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[ForkSessionResult]{Status: current.Status, Message: current.Message}, nil
	}
	forked, err := m.deps.Sessions.Fork(ctx, m.deps.Scope(ctx), req.MessageID)
	if err != nil {
		return operationresult.Result[ForkSessionResult]{}, err
	}
	if forked.Status != operationresult.Success {
		return operationresult.Result[ForkSessionResult]{Status: forked.Status, Message: forked.Message}, nil
	}
	row := forked.Data
	record := events.LogRecord{Category: events.LogCategoryAudit, Level: slog.LevelInfo, EventName: "session_fork", Module: "control", Content: "parent: " + row.ParentSessionID + " · message: " + row.ForkFromMessageID, ResultStatus: events.ResultSucceeded, Identity: events.LogIdentityFromContext(ctx)}
	record.Identity.SessionID = row.ID
	events.Log(ctx, record)
	return m.sessionPreview(ctx, row, req.MessageLimit)
}

func (m *Supervisor) CompactSession(ctx context.Context, req CompactSessionRequest) (operationresult.Result[CompactSessionResult], error) {
	ready, err := m.beforeSession(ctx)
	if err != nil {
		return operationresult.Result[CompactSessionResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[CompactSessionResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	ctx = ready.Data
	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[CompactSessionResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[CompactSessionResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	current, err := m.requireCurrent(ctx, resolved.Data)
	if err != nil {
		return operationresult.Result[CompactSessionResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[CompactSessionResult]{Status: current.Status, Message: current.Message}, nil
	}
	compacted, err := m.deps.Execution.CompactCurrent(ctx, resolved.Data)
	if err != nil {
		return operationresult.Result[CompactSessionResult]{}, err
	}
	if compacted.Status != operationresult.Success {
		return operationresult.Result[CompactSessionResult]{Status: compacted.Status, Message: compacted.Message}, nil
	}
	row := compacted.Data
	return operationresult.Result[CompactSessionResult]{Status: operationresult.Success, Data: CompactSessionResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}}, nil
}

func (m *Supervisor) SetMode(ctx context.Context, req SetModeRequest) (operationresult.Result[SetModeResult], error) {
	ready, err := m.beforeSession(ctx)
	if err != nil {
		return operationresult.Result[SetModeResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[SetModeResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	ctx = ready.Data
	activated, err := m.deps.Sessions.ActivateMode(ctx, m.deps.Scope(ctx), session.ActivateModeRequest{Mode: req.Mode, NewSessionTitle: req.NewSessionTitle})
	if err != nil {
		return operationresult.Result[SetModeResult]{}, err
	}
	if activated.Status != operationresult.Success {
		return operationresult.Result[SetModeResult]{Status: activated.Status, Message: activated.Message}, nil
	}
	row := activated.Data.Session
	result := SetModeResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1, Unchanged: activated.Data.AlreadyActive}
	if req.Text != "" {
		result.Continuation = &Continuation{Kind: "input", SessionID: row.ID, Text: req.Text}
	}
	return operationresult.Result[SetModeResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) SessionMessages(ctx context.Context, req SessionMessagesRequest) (operationresult.Result[SessionMessagesResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[SessionMessagesResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[SessionMessagesResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	resolved, err := m.resolveSessionID(ctx, req.SessionID)
	if err != nil {
		return operationresult.Result[SessionMessagesResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[SessionMessagesResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	row, err := m.readSession(ctx, resolved.Data)
	if err != nil {
		return operationresult.Result[SessionMessagesResult]{}, err
	}
	if row.Status != operationresult.Success {
		return operationresult.Result[SessionMessagesResult]{Status: row.Status, Message: row.Message}, nil
	}
	if req.Page < 1 || req.PageSize < 1 {
		return operationresult.Result[SessionMessagesResult]{Status: operationresult.InvalidArguments, Message: "page 和 page_size 必须为正数"}, nil
	}
	loaded, err := m.deps.History.LoadMessagePage(ctx, resolved.Data, req.Page, req.PageSize)
	if err != nil {
		return operationresult.Result[SessionMessagesResult]{}, err
	}
	if req.Page > max(1, loaded.TotalPages) {
		return operationresult.Result[SessionMessagesResult]{Status: operationresult.InvalidArguments, Message: "page 超出可用页数"}, nil
	}
	items, err := messageData(loaded.Messages)
	if err != nil {
		return operationresult.Result[SessionMessagesResult]{}, err
	}
	result := SessionMessagesResult{SessionID: resolved.Data, Items: items, Page: Page{Page: req.Page, PageSize: req.PageSize, HasNext: req.Page < loaded.TotalPages, TotalPages: loaded.TotalPages}}
	return operationresult.Result[SessionMessagesResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) beforeSession(ctx context.Context) (operationresult.Result[context.Context], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[context.Context]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[context.Context]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	current, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[context.Context]{}, err
	}
	if current.Status == operationresult.NoCurrentSession {
		return operationresult.Result[context.Context]{Status: operationresult.Success, Data: ctx}, nil
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[context.Context]{Status: current.Status, Message: current.Message}, nil
	}
	binding := current.Data.Binding
	if original, ok := session.BindingFromContext(ctx); ok && (original != binding || !original.Valid()) {
		return operationresult.Result[context.Context]{Status: operationresult.Conflict, Message: "原会话绑定已经失效"}, nil
	}
	return operationresult.Result[context.Context]{Status: operationresult.Success, Data: session.WithBinding(ctx, binding)}, nil
}

func (m *Supervisor) resolveSessionID(ctx context.Context, id string) (operationresult.Result[string], error) {
	id = strings.TrimSpace(id)
	if id != "" {
		return operationresult.Result[string]{Status: operationresult.Success, Data: id}, nil
	}
	current, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[string]{Status: current.Status, Message: current.Message}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: current.Data.ID}, nil
}

func (m *Supervisor) readSession(ctx context.Context, id string) (operationresult.Result[*storage.Session], error) {
	if strings.TrimSpace(id) == "" {
		return operationresult.Result[*storage.Session]{Status: operationresult.InvalidArguments, Message: "session_id 不能为空"}, nil
	}
	row, err := m.deps.History.GetSession(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "目标不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	if actor.Role != contextinfo.RoleSuperadmin && !session.CanAccess(m.deps.Scope(ctx), row) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "无权访问该会话"}, nil
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}

func (m *Supervisor) requireCurrent(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	if strings.TrimSpace(id) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "session_id 不能为空"}, nil
	}
	current, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: current.Status, Message: current.Message}, nil
	}
	if current.Data.ID != id {
		return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "目标不是当前绑定的会话"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (m *Supervisor) currentID(ctx context.Context) (operationresult.Result[string], error) {
	current, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if current.Status == operationresult.NoCurrentSession {
		return operationresult.Result[string]{Status: operationresult.Success}, nil
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[string]{Status: current.Status, Message: current.Message}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: current.Data.ID}, nil
}

func (m *Supervisor) sessionChange(ctx context.Context, row *storage.Session, id string, count int) (operationresult.Result[SessionChangeResult], error) {
	current, err := m.currentID(ctx)
	if err != nil {
		return operationresult.Result[SessionChangeResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[SessionChangeResult]{Status: current.Status, Message: current.Message}, nil
	}
	result := SessionChangeResult{Session: sessionData(row), SessionID: id, CurrentSessionID: current.Data, AffectedCount: count}
	return operationresult.Result[SessionChangeResult]{Status: operationresult.Success, Data: result}, nil
}

func sessionData(row *storage.Session) *SessionData {
	if row == nil {
		return nil
	}
	result := &SessionData{ID: row.ID, Title: row.Title, Mode: row.Mode, Status: row.Status, Platform: row.Platform, ChatID: row.ChatID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ParentSessionID: row.ParentSessionID, ForkFromMessageID: row.ForkFromMessageID}
	if row.ArchivedAt != nil {
		result.ArchivedAt = new(*row.ArchivedAt)
	}
	if row.PinnedAt != nil {
		result.PinnedAt = new(*row.PinnedAt)
	}
	return result
}

func messageData(rows []storage.Message) ([]MessageData, error) {
	result := make([]MessageData, 0, len(rows))
	for _, row := range rows {
		item := MessageData{ID: row.ID, SessionID: row.SessionID, Role: row.Role, Content: row.Content, SourceContent: row.SourceContent, ReasoningContent: row.ReasoningContent, ParentMessageID: row.ParentMessageID, ReplyToPlatformMessageID: row.ReplyToPlatformMessageID, ReplyToMessageID: row.ReplyToMessageID, ToolCallID: row.ToolCallID, ToolCalls: make([]ToolCallData, len(row.ToolCalls)), ToolName: row.ToolName, ToolBatchStatus: row.ToolBatchStatus, ToolResultStatus: row.ToolResultStatus, CreatedAt: row.CreatedAt}
		for i, call := range row.ToolCalls {
			item.ToolCalls[i] = ToolCallData{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		}
		var segments []llm.MessageSegment
		if row.Segments != "" {
			if err := json.Unmarshal([]byte(row.Segments), &segments); err != nil {
				return nil, err
			}
		}
		item.Segments = segments
		result = append(result, item)
	}
	return result, nil
}

func (m *Supervisor) sessionPreview(ctx context.Context, row *storage.Session, limit int) (operationresult.Result[SessionChangeResult], error) {
	result := SessionChangeResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}
	if limit == 0 {
		return operationresult.Result[SessionChangeResult]{Status: operationresult.Success, Data: result}, nil
	}
	messages, err := m.SessionMessages(ctx, SessionMessagesRequest{SessionID: row.ID, Page: 1, PageSize: limit})
	if err != nil {
		return operationresult.Result[SessionChangeResult]{}, err
	}
	if messages.Status != operationresult.Success {
		return operationresult.Result[SessionChangeResult]{Status: messages.Status, Message: messages.Message}, nil
	}
	result.Messages = messages.Data.Items
	return operationresult.Result[SessionChangeResult]{Status: operationresult.Success, Data: result}, nil
}
