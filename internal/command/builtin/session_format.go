package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/llm"
	"encoding/json"
	"fmt"

	"elbot/internal/contextmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"errors"
)

func listSessionPage(ctx context.Context, deps Deps, query string, page int, archivedOnly, excludeCurrent bool) (session.ListResult, error) {
	result, err := deps.Sessions.ListPage(ctx, deps.Scope(ctx), session.ListRequest{Query: query, Page: page, PageSize: deps.SessionListPageSize, ArchivedOnly: archivedOnly, ExcludeCurrent: excludeCurrent})
	if err != nil {
		if errors.Is(err, session.ErrInvalidPage) {
			return result, inputError(err)
		}
		return result, err
	}
	return result, nil
}

func nextPageCommand(commandText func(string, ...string) string, name string, page int, query string) string {
	if name == "resume" {
		return commandText(name, "--page", fmt.Sprint(page))
	}
	if query != "" {
		return commandText(name, fmt.Sprint(page), "-k", query)
	}
	return commandText(name, fmt.Sprint(page))
}

func currentSessionID(ctx context.Context, deps Deps) string {
	current, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
	if err != nil {
		return ""
	}
	return current.ID
}

func currentBoundSessionID(ctx context.Context, deps Deps) (string, error) {
	current, _, err := deps.Sessions.CurrentBound(ctx, deps.Scope(ctx))
	if errors.Is(err, session.ErrNoCurrentSession) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return current.ID, nil
}

func recentConversationMessages(ctx context.Context, deps Deps, sessionID string, turns int) ([]storage.Message, error) {
	loaded, err := (contextmgr.Loader{Store: deps.Store}).LoadMessagePage(ctx, sessionID, 1, turns*2)
	if err != nil {
		return nil, err
	}
	return loaded.Messages, nil
}

func sessionData(s *storage.Session) *command.SessionData {
	if s == nil {
		return nil
	}
	return &command.SessionData{ID: s.ID, Title: s.Title, Mode: s.Mode, Status: s.Status, Platform: s.Platform, ChatID: s.ChatID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ArchivedAt: s.ArchivedAt, PinnedAt: s.PinnedAt, ParentSessionID: s.ParentSessionID, ForkFromMessageID: s.ForkFromMessageID}
}

func sessionChange(ctx context.Context, deps Deps, row *storage.Session, id string, count int, operation string) *command.Result {
	return &command.Result{Kind: command.ResultKindSession, Data: command.SessionChangeData{Session: sessionData(row), SessionID: id, AffectedCount: count, CurrentSessionID: currentSessionID(ctx, deps), Operation: operation}}
}

func sessionChangeAtCurrent(row *storage.Session, id string, count int, currentID, operation string) *command.Result {
	return &command.Result{Kind: command.ResultKindSession, Data: command.SessionChangeData{Session: sessionData(row), SessionID: id, AffectedCount: count, CurrentSessionID: currentID, Operation: operation}}
}

func sessionsResult(ctx context.Context, deps Deps, page session.ListResult, query, listType string) (*command.Result, error) {
	current, err := currentBoundSessionID(ctx, deps)
	if err != nil {
		return nil, err
	}
	prevCommand, nextCommand := "", ""
	if page.Page > 1 {
		prevCommand = nextPageCommand(deps.CommandText, listType, page.Page-1, query)
	}
	if page.Page < page.TotalPages {
		nextCommand = nextPageCommand(deps.CommandText, listType, page.Page+1, query)
	}
	items := make([]command.SessionSummaryData, 0, len(page.Items))
	for _, s := range page.Items {
		messages, err := messageData(s.Messages)
		if err != nil {
			return nil, err
		}
		items = append(items, command.SessionSummaryData{
			SessionData:  command.SessionData{ID: s.ID, Title: s.Title, Mode: s.Mode, Status: s.Status, Platform: s.Platform, ChatID: s.ChatID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ArchivedAt: s.ArchivedAt, PinnedAt: s.PinnedAt, ParentSessionID: s.ParentSessionID, ForkFromMessageID: s.ForkFromMessageID},
			MessageCount: s.MessageCount, Messages: messages, LastUserContent: s.LastUserContent, LastAssistantContent: s.LastAssistantContent,
		})
	}
	return &command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{Page: command.Page{Page: page.Page, PageSize: page.PageSize, HasNext: nextCommand != "", TotalPages: &page.TotalPages}, Items: items, CurrentSessionID: current, Query: query, ListType: listType, PrevCommand: prevCommand, NextCommand: nextCommand}}, nil
}

func messageData(rows []storage.Message) ([]command.MessageData, error) {
	out := make([]command.MessageData, 0, len(rows))
	for _, m := range rows {
		attachments := []command.AttachmentData{}
		var segments []llm.MessageSegment
		if m.Segments != "" {
			if err := json.Unmarshal([]byte(m.Segments), &segments); err != nil {
				return nil, fmt.Errorf("decode message %s segments: %w", m.ID, err)
			}
			for _, segment := range segments {
				if segment.Type == llm.SegmentFile || segment.Type == llm.SegmentImage {
					attachments = append(attachments, command.AttachmentData{Name: segment.Name})
				}
			}
		}
		out = append(out, command.MessageData{ID: m.ID, Role: llm.MessageRole(m.Role), Content: m.Content, Attachments: attachments, CreatedAt: m.CreatedAt})
	}
	return out, nil
}
