package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/llm"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"errors"
)

func toolsString(tools map[string]int) string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", name, tools[name]))
	}
	return strings.Join(parts, ", ")
}

func sessionPageSize(deps Deps) int {
	pageSize, _ := deps.SessionState.config()
	if pageSize > 0 {
		return pageSize
	}
	return defaultSessionListPageSize
}

func listSessionPage(ctx context.Context, deps Deps, query string, page, pageSize int, archivedOnly bool) ([]storage.SessionSummary, bool, error) {
	sessions, err := deps.Sessions.ListPage(ctx, deps.Scope(ctx), query, pageSize+1, (page-1)*pageSize, archivedOnly)
	if err != nil {
		return nil, false, err
	}
	hasNext := len(sessions) > pageSize
	if hasNext {
		sessions = sessions[:pageSize]
	}
	return sessions, hasNext, nil
}

func listResumablePage(ctx context.Context, deps Deps, page, pageSize int) ([]storage.SessionSummary, bool, error) {
	sessions, err := deps.Sessions.ListResumablePage(ctx, deps.Scope(ctx), pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, false, err
	}
	hasNext := len(sessions) > pageSize
	if hasNext {
		sessions = sessions[:pageSize]
	}
	return sessions, hasNext, nil
}

func formatSessionsPage(sessions []storage.SessionSummary, currentID string, page int, query string, hasNext bool, commandText func(string, ...string) string, name string) string {
	return formatSessionsPageWithOffset(sessions, currentID, page, query, hasNext, commandText, name, 0)
}

func formatResumableSessionsPage(sessions []storage.SessionSummary, page, pageSize int, hasNext bool, commandText func(string, ...string) string) string {
	return formatSessionsPageWithOffset(sessions, "", page, "", hasNext, commandText, "resume", (page-1)*pageSize)
}

func formatSessionsPageWithOffset(sessions []storage.SessionSummary, currentID string, page int, query string, hasNext bool, commandText func(string, ...string) string, name string, numberOffset int) string {
	content := formatSessionsWithOffset(sessions, currentID, numberOffset)
	var sb strings.Builder
	sb.WriteString(content)
	if content != "" {
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("page=%d", page))
	if page > 1 {
		sb.WriteString(fmt.Sprintf(" · prev: %s", nextPageCommand(commandText, name, page-1, query)))
	}
	if hasNext {
		sb.WriteString(fmt.Sprintf(" · next: %s", nextPageCommand(commandText, name, page+1, query)))
	}
	return trimTrailingNewlines(sb.String())
}

func nextPageCommand(commandText func(string, ...string) string, name string, page int, query string) string {
	if name == "resume" {
		return commandText(name, "--page", fmt.Sprint(page))
	}
	return commandText(name, fmt.Sprint(page), query)
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

func formatResumeResult(session *storage.Session, history []storage.Message) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("resume → %s · %s\n", session.Title, session.ID))
	appendRecentMessages(&sb, history)
	return trimTrailingNewlines(sb.String())
}

func formatForkResult(session *storage.Session, history []storage.Message) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fork → %s · %s\nparent=%s · from=%s\n", session.ID, session.Mode, session.ParentSessionID, session.ForkFromMessageID))
	appendRecentMessages(&sb, history)
	return trimTrailingNewlines(sb.String())
}

func appendRecentMessages(sb *strings.Builder, history []storage.Message) {
	if len(history) == 0 {
		return
	}
	for _, message := range history {
		role := message.Role
		if role == storage.RoleAssistant {
			role = "bot"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", role, previewContent(message.Content)))
	}
}

func recentConversationMessages(ctx context.Context, deps Deps, sessionID string, turns int) ([]storage.Message, error) {
	loaded, err := (contextmgr.Loader{Store: deps.Store}).LoadMessagePage(ctx, sessionID, 1, turns*2)
	if err != nil {
		return nil, err
	}
	return loaded.Messages, nil
}

func previewContent(text string) string {
	return truncateRunes(strings.TrimSpace(strings.ReplaceAll(text, "\n", " ")), 80)
}

func messagePreview(text string) string {
	return truncateRunes(strings.TrimSpace(strings.Join(strings.Fields(text), " ")), 40)
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "..."
}

func formatSessionsWithOffset(sessions []storage.SessionSummary, currentID string, numberOffset int) string {
	var sb strings.Builder
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("[%d] %s%s\n%s · %s/%s · %s · msgs=%d\n", numberOffset+i+1, title, sessionMarkers(s, currentID), s.ID, s.Platform, s.ChatID, formatTime(s.UpdatedAt), s.MessageCount))
		if preview := sessionMessagePreview(s.Messages); preview != "" {
			sb.WriteString(preview + "\n")
		}
	}
	return trimTrailingNewlines(sb.String())
}

func sessionMarkers(s storage.SessionSummary, currentID string) string {
	markers := []string{}
	if s.ID == currentID {
		markers = append(markers, "current")
	}
	if s.PinnedAt != nil {
		markers = append(markers, "pinned")
	}
	if s.ArchivedAt != nil {
		markers = append(markers, "archived")
	}
	if len(markers) == 0 {
		return ""
	}
	return " [" + strings.Join(markers, ", ") + "]"
}

func formatEmptyStatus(ctx context.Context, deps Deps) string {
	scope := deps.Scope(ctx)
	modeModel := deps.Models.CurrentModelForMode(deps.Sessions.DefaultMode())
	compactModel := deps.Models.CurrentCompactModel(deps.Sessions.DefaultMode())
	return trimTrailingNewlines(fmt.Sprintf(`session=nil
default=%s · model=%s/%s
compact.model=%s/%s
platform=%s/%s
phase=idle · pending=0
`, deps.Sessions.DefaultMode(), modeModel.Provider, modeModel.Model, compactModel.Provider, compactModel.Model, scope.Platform, scope.ChatID))
}

func indentStatusBlock(text string) string {
	var sb strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			sb.WriteString("  " + line + "\n")
		}
	}
	return sb.String()
}

func formatTime(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

func emptyTODO(s string) string {
	if s == "" {
		return "nil"
	}
	return s
}

func sessionMessagePreview(messages []storage.Message) string {
	var parts []string
	for i, message := range messages {
		if i == 4 {
			break
		}
		content := strings.TrimSpace(strings.ReplaceAll(message.Content, "\n", " "))
		if content == "" {
			continue
		}
		runes := []rune(content)
		if len(runes) > 24 {
			content = string(runes[:24]) + "..."
		}
		role := "user"
		if message.Role == storage.RoleAssistant {
			role = "bot"
		}
		parts = append(parts, role+": "+content)
	}
	if len(parts) > 0 && len(messages) > 4 {
		parts = append(parts, "...")
	}
	return strings.Join(parts, " / ")
}

func sessionStatusPreview(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:80]) + "..."
	}
	return text
}

func sessionData(s *storage.Session) *command.SessionData {
	if s == nil {
		return nil
	}
	return &command.SessionData{ID: s.ID, Title: s.Title, Mode: s.Mode, Status: s.Status, Platform: s.Platform, ChatID: s.ChatID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ArchivedAt: s.ArchivedAt, PinnedAt: s.PinnedAt, ParentSessionID: s.ParentSessionID, ForkFromMessageID: s.ForkFromMessageID}
}

func sessionChange(ctx context.Context, deps Deps, row *storage.Session, id string, count int, text string) *command.Result {
	return &command.Result{Kind: command.ResultKindSession, Text: text, Data: command.SessionChangeData{Session: sessionData(row), SessionID: id, AffectedCount: count, CurrentSessionID: currentSessionID(ctx, deps)}}
}

func sessionChangeAtCurrent(row *storage.Session, id string, count int, text, currentID string) *command.Result {
	return &command.Result{Kind: command.ResultKindSession, Text: text, Data: command.SessionChangeData{Session: sessionData(row), SessionID: id, AffectedCount: count, CurrentSessionID: currentID}}
}

func sessionsResult(rows []storage.SessionSummary, current string, page, size int, query string, hasNext bool, text string) (*command.Result, error) {
	items := make([]command.SessionSummaryData, 0, len(rows))
	for _, s := range rows {
		messages, err := messageData(s.Messages)
		if err != nil {
			return nil, err
		}
		items = append(items, command.SessionSummaryData{
			SessionData:  command.SessionData{ID: s.ID, Title: s.Title, Mode: s.Mode, Status: s.Status, Platform: s.Platform, ChatID: s.ChatID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ArchivedAt: s.ArchivedAt, PinnedAt: s.PinnedAt, ParentSessionID: s.ParentSessionID, ForkFromMessageID: s.ForkFromMessageID},
			MessageCount: s.MessageCount, Messages: messages, LastUserContent: s.LastUserContent, LastAssistantContent: s.LastAssistantContent,
		})
	}
	return &command.Result{Kind: command.ResultKindSessions, Text: text, Data: command.SessionsData{Page: command.Page{Page: page, PageSize: size, HasNext: hasNext}, Items: items, CurrentSessionID: current, Query: query}}, nil
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
