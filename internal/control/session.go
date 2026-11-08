package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResult, error) {
	result := ListSessionsResult{Items: []SessionSummaryData{}, Query: req.Query}
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return result, err
	}
	listed, err := m.deps.Sessions.ListPage(ctx, m.deps.Scope(ctx), session.ListRequest{Query: req.Query, Page: req.Page, PageSize: req.PageSize, ArchivedOnly: req.Archived, ExcludeCurrent: req.ExcludeCurrent})
	if err != nil {
		return result, ClassifyError(err)
	}
	result.Page = Page{Page: listed.Page, PageSize: listed.PageSize, TotalPages: listed.TotalPages, HasNext: listed.Page < listed.TotalPages}
	result.CurrentSessionID, err = m.currentID(ctx)
	if err != nil {
		return result, err
	}
	for _, row := range listed.Items {
		messages, err := messageData(row.Messages)
		if err != nil {
			return result, ClassifyError(err)
		}
		result.Items = append(result.Items, SessionSummaryData{SessionData: *sessionData(&storage.Session{ID: row.ID, Title: row.Title, Mode: row.Mode, Status: row.Status, Platform: row.Platform, ChatID: row.ChatID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt, PinnedAt: row.PinnedAt, ParentSessionID: row.ParentSessionID, ForkFromMessageID: row.ForkFromMessageID}), MessageCount: row.MessageCount, Messages: messages, LastUserContent: row.LastUserContent, LastAssistantContent: row.LastAssistantContent})
	}
	return result, nil
}

func (m *Supervisor) GetSession(ctx context.Context, req GetSessionRequest) (GetSessionResult, error) {
	if err := authorize(ctx, false, m.deps.History != nil); err != nil {
		return GetSessionResult{}, err
	}
	row, err := m.readSession(ctx, req.SessionID)
	if err != nil {
		return GetSessionResult{}, err
	}
	return *sessionData(row), nil
}

func (m *Supervisor) NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResult, error) {
	ctx, err := m.beforeSession(ctx)
	if err != nil {
		return NewSessionResult{}, err
	}
	row, err := m.deps.Sessions.Create(ctx, m.deps.Scope(ctx), session.CreateRequest{Mode: req.Mode, Title: req.Title})
	if err != nil {
		return NewSessionResult{}, ClassifyError(err)
	}
	return SessionChangeResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}, nil
}

func (m *Supervisor) RenameSession(ctx context.Context, req RenameSessionRequest) (RenameSessionResult, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return RenameSessionResult{}, err
	}
	row, err := m.deps.Sessions.Rename(ctx, m.deps.Scope(ctx), req.SessionID, req.Title)
	if err != nil {
		return RenameSessionResult{}, ClassifyError(err)
	}
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) SetSessionArchived(ctx context.Context, req SetSessionArchivedRequest) (SetSessionArchivedResult, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return SetSessionArchivedResult{}, err
	}
	if req.Archived && !req.Confirmed {
		return SetSessionArchivedResult{}, &Error{Code: "confirmation_required", Message: "归档操作需要确认"}
	}
	var row *storage.Session
	var err error
	if req.Archived {
		row, err = m.deps.Sessions.Archive(ctx, m.deps.Scope(ctx), req.SessionID)
	} else {
		row, err = m.deps.Sessions.Unarchive(ctx, m.deps.Scope(ctx), req.SessionID)
	}
	if err != nil {
		return SetSessionArchivedResult{}, ClassifyError(err)
	}
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) SetSessionPinned(ctx context.Context, req SetSessionPinnedRequest) (SetSessionPinnedResult, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return SetSessionPinnedResult{}, err
	}
	var row *storage.Session
	var err error
	if req.Pinned {
		row, err = m.deps.Sessions.Pin(ctx, m.deps.Scope(ctx), req.SessionID)
	} else {
		row, err = m.deps.Sessions.Unpin(ctx, m.deps.Scope(ctx), req.SessionID)
	}
	if err != nil {
		return SetSessionPinnedResult{}, ClassifyError(err)
	}
	return m.sessionChange(ctx, row, row.ID, 1)
}

func (m *Supervisor) DeleteSession(ctx context.Context, req DeleteSessionRequest) (DeleteSessionResult, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return DeleteSessionResult{}, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return DeleteSessionResult{}, invalid("session_id 不能为空")
	}
	if !req.Confirmed {
		return DeleteSessionResult{}, &Error{Code: "confirmation_required", Message: "删除操作需要确认"}
	}
	if err := m.deps.Sessions.Delete(ctx, m.deps.Scope(ctx), req.SessionID); err != nil {
		return DeleteSessionResult{}, ClassifyError(err)
	}
	return m.sessionChange(ctx, nil, req.SessionID, 1)
}

func (m *Supervisor) CleanSessions(ctx context.Context, req CleanSessionsRequest) (CleanSessionsResult, error) {
	if err := authorize(ctx, true, m.deps.Sessions != nil); err != nil {
		return CleanSessionsResult{}, err
	}
	if !req.Confirmed {
		return CleanSessionsResult{}, &Error{Code: "confirmation_required", Message: "清理操作需要确认"}
	}
	count, err := m.deps.Sessions.CleanupExpired(ctx, req.Before)
	return CleanSessionsResult{AffectedCount: count}, ClassifyError(err)
}

func (m *Supervisor) ResumeSession(ctx context.Context, req ResumeSessionRequest) (ResumeSessionResult, error) {
	ctx, err := m.beforeSession(ctx)
	if err != nil {
		return ResumeSessionResult{}, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return ResumeSessionResult{}, invalid("session_id 不能为空")
	}
	var row *storage.Session
	if req.SessionID == "last" {
		row, err = m.deps.Sessions.ResumeLast(ctx, m.deps.Scope(ctx))
	} else {
		row, err = m.deps.Sessions.Resume(ctx, m.deps.Scope(ctx), req.SessionID)
	}
	if err != nil {
		return ResumeSessionResult{}, ClassifyError(err)
	}
	return ResumeSessionResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}, nil
}

func (m *Supervisor) ForkSession(ctx context.Context, req ForkSessionRequest) (ForkSessionResult, error) {
	ctx, err := m.beforeSession(ctx)
	if err != nil {
		return ForkSessionResult{}, err
	}
	if strings.TrimSpace(req.MessageID) == "" {
		return ForkSessionResult{}, invalid("message_id 不能为空")
	}
	if err := m.requireCurrent(ctx, req.SessionID); err != nil {
		return ForkSessionResult{}, err
	}
	row, err := m.deps.Sessions.Fork(ctx, m.deps.Scope(ctx), req.MessageID)
	if err != nil {
		return ForkSessionResult{}, ClassifyError(err)
	}
	return ForkSessionResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}, nil
}

func (m *Supervisor) CompactSession(ctx context.Context, req CompactSessionRequest) (CompactSessionResult, error) {
	ctx, err := m.beforeSession(ctx)
	if err != nil {
		return CompactSessionResult{}, err
	}
	if err := m.requireCurrent(ctx, req.SessionID); err != nil {
		return CompactSessionResult{}, err
	}
	if m.deps.Execution == nil {
		return CompactSessionResult{}, &Error{Code: "not_configured", Message: "会话压缩尚未接入"}
	}
	row, err := m.deps.Execution.CompactCurrent(ctx, req.SessionID)
	if err != nil {
		return CompactSessionResult{}, ClassifyError(err)
	}
	return CompactSessionResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1}, nil
}

func (m *Supervisor) SetMode(ctx context.Context, req SetModeRequest) (SetModeResult, error) {
	ctx, err := m.beforeSession(ctx)
	if err != nil {
		return SetModeResult{}, err
	}
	activated, err := m.deps.Sessions.ActivateMode(ctx, m.deps.Scope(ctx), session.ActivateModeRequest{Mode: req.Mode, NewSessionTitle: req.NewSessionTitle})
	if err != nil {
		return SetModeResult{}, ClassifyError(err)
	}
	row := activated.Session
	result := SetModeResult{Session: sessionData(row), SessionID: row.ID, CurrentSessionID: row.ID, AffectedCount: 1, Unchanged: activated.AlreadyActive}
	if req.Text != "" {
		result.Continuation = &Continuation{SessionID: row.ID, Text: req.Text}
	}
	return result, nil
}

func (m *Supervisor) SessionMessages(ctx context.Context, req SessionMessagesRequest) (SessionMessagesResult, error) {
	result := SessionMessagesResult{SessionID: req.SessionID, Items: []MessageData{}}
	if err := authorize(ctx, false, m.deps.History != nil); err != nil {
		return result, err
	}
	if _, err := m.readSession(ctx, req.SessionID); err != nil {
		return result, err
	}
	if req.Page < 1 || req.PageSize < 1 {
		return result, invalid("page 和 page_size 必须为正数")
	}
	loaded, err := m.deps.History.LoadMessagePage(ctx, req.SessionID, req.Page, req.PageSize)
	if err != nil {
		return result, ClassifyError(err)
	}
	if req.Page > max(1, loaded.TotalPages) {
		return result, invalid("page 超出可用页数")
	}
	result.Page = Page{Page: req.Page, PageSize: req.PageSize, HasNext: req.Page < loaded.TotalPages, TotalPages: loaded.TotalPages}
	result.Items, err = messageData(loaded.Messages)
	return result, ClassifyError(err)
}

func (m *Supervisor) beforeSession(ctx context.Context) (context.Context, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil); err != nil {
		return ctx, err
	}
	_, binding, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
	if err != nil && !errors.Is(err, session.ErrNoCurrentSession) {
		return ctx, ClassifyError(err)
	}
	if original, ok := session.BindingFromContext(ctx); ok && (original != binding || !original.Valid()) {
		return ctx, &Error{Code: "binding_changed", Message: "原会话绑定已经失效"}
	}
	if errors.Is(err, session.ErrNoCurrentSession) {
		return ctx, nil
	}
	return session.WithBinding(ctx, binding), nil
}

func (m *Supervisor) readSession(ctx context.Context, id string) (*storage.Session, error) {
	if strings.TrimSpace(id) == "" {
		return nil, invalid("session_id 不能为空")
	}
	if m.deps.Scope == nil {
		return nil, &Error{Code: "not_configured", Message: "会话访问范围尚未接入"}
	}
	row, err := m.deps.History.GetSession(ctx, id)
	if err != nil {
		return nil, ClassifyError(err)
	}
	if !session.CanAccess(m.deps.Scope(ctx), row) {
		return nil, ClassifyError(session.ErrAccessDenied)
	}
	return row, nil
}

func (m *Supervisor) requireCurrent(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return invalid("session_id 不能为空")
	}
	current, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if err != nil {
		return ClassifyError(err)
	}
	if current.ID != id {
		return &Error{Code: "binding_changed", Message: "目标不是当前绑定的会话"}
	}
	return nil
}

func (m *Supervisor) currentID(ctx context.Context) (string, error) {
	row, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if errors.Is(err, session.ErrNoCurrentSession) {
		return "", nil
	}
	if err != nil {
		return "", ClassifyError(err)
	}
	return row.ID, nil
}

func (m *Supervisor) sessionChange(ctx context.Context, row *storage.Session, id string, count int) (SessionChangeResult, error) {
	result := SessionChangeResult{Session: sessionData(row), SessionID: id, AffectedCount: count}
	var err error
	result.CurrentSessionID, err = m.currentID(ctx)
	return result, err
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
		item := MessageData{ID: row.ID, SessionID: row.SessionID, Role: row.Role, Content: row.Content, ParentMessageID: row.ParentMessageID, ReplyToPlatformMessageID: row.ReplyToPlatformMessageID, ReplyToMessageID: row.ReplyToMessageID, ToolCallID: row.ToolCallID, Metadata: row.Metadata, CreatedAt: row.CreatedAt}
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
