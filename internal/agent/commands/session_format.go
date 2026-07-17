package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/storage"
)

func sessionPageSize(deps Deps) int {
	pageSize, _ := deps.SessionState.config()
	if pageSize > 0 {
		return pageSize
	}
	return defaultSessionListPageSize
}

func listSessionPage(ctx context.Context, deps Deps, query string, page, pageSize int, archivedOnly bool) ([]storage.SessionSummary, bool, error) {
	if page < 1 {
		return nil, false, fmt.Errorf("page must be a positive number")
	}
	if pageSize <= 0 {
		pageSize = defaultSessionListPageSize
	}
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
	if page < 1 {
		return nil, false, fmt.Errorf("page must be a positive number")
	}
	if pageSize <= 0 {
		pageSize = defaultSessionListPageSize
	}
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

func formatSessionsPage(sessions []storage.SessionSummary, currentID string, page int, query string, hasNext bool, commandPrefix string) string {
	return formatSessionsPageWithOffset(sessions, currentID, page, query, hasNext, commandPrefix, 0)
}

func formatResumableSessionsPage(sessions []storage.SessionSummary, page, pageSize int, hasNext bool) string {
	return formatSessionsPageWithOffset(sessions, "", page, "", hasNext, "/resume --page", (page-1)*pageSize)
}

func formatSessionsPageWithOffset(sessions []storage.SessionSummary, currentID string, page int, query string, hasNext bool, commandPrefix string, numberOffset int) string {
	content := formatSessionsWithOffset(sessions, currentID, numberOffset)
	var sb strings.Builder
	sb.WriteString(content)
	if content != "" {
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("page: %d\n", page))
	if page > 1 {
		sb.WriteString(fmt.Sprintf("prev: %s\n", nextPageCommand(commandPrefix, page-1, query)))
	}
	if hasNext {
		sb.WriteString(fmt.Sprintf("next: %s\n", nextPageCommand(commandPrefix, page+1, query)))
	}
	return trimTrailingNewlines(sb.String())
}

func nextPageCommand(commandPrefix string, page int, query string) string {
	if commandPrefix == "/resume --page" || strings.TrimSpace(query) == "" {
		return fmt.Sprintf("%s %d", commandPrefix, page)
	}
	return fmt.Sprintf("%s %d %s", commandPrefix, page, query)
}

func currentSessionID(ctx context.Context, deps Deps) string {
	current, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
	if err != nil {
		return ""
	}
	return current.ID
}

func formatResumeResult(ctx context.Context, deps Deps, session *storage.Session) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("resumed session:\n  id: %s\n  title: %s\n", session.ID, session.Title))
	appendRecentMessages(&sb, ctx, deps, session.ID)
	return trimTrailingNewlines(sb.String())
}

func formatForkResult(ctx context.Context, deps Deps, session *storage.Session) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("forked session:\n  id: %s\n  parent: %s\n  from message: %s\n  mode: %s\n", session.ID, session.ParentSessionID, session.ForkFromMessageID, session.Mode))
	appendRecentMessages(&sb, ctx, deps, session.ID)
	return trimTrailingNewlines(sb.String())
}

func appendRecentMessages(sb *strings.Builder, ctx context.Context, deps Deps, sessionID string) {
	history := recentConversationMessages(ctx, deps, sessionID, resumeHistoryTurns)
	if len(history) == 0 {
		return
	}
	sb.WriteString("recent messages:\n")
	for _, message := range history {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", message.Role, previewContent(message.Content)))
	}
}

func recentConversationMessages(ctx context.Context, deps Deps, sessionID string, turns int) []storage.Message {
	loaded, err := (contextmgr.Loader{Store: deps.Store}).Load(ctx, sessionID)
	if err != nil {
		return nil
	}
	limit := turns * 2
	out := make([]storage.Message, 0, limit)
	for i := len(loaded.Messages) - 1; i >= 0 && len(out) < limit; i-- {
		message := loaded.Messages[i]
		if (message.Role == storage.RoleUser || message.Role == storage.RoleAssistant) && strings.TrimSpace(message.Content) != "" {
			out = append(out, message)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func previewContent(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
}

func assistantMessages(messages []storage.Message) []storage.Message {
	out := make([]storage.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == storage.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			out = append(out, message)
		}
	}
	return out
}

func formatMessagePage(messages []storage.Message, page, pageSize int) (string, error) {
	if pageSize <= 0 {
		pageSize = messageListPageSize
	}
	totalPages := (len(messages) + pageSize - 1) / pageSize
	if page < 1 || page > totalPages {
		return "", fmt.Errorf("page %d out of range; available pages: 1-%d", page, totalPages)
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, len(messages))
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("messages page %d/%d:\n", page, totalPages))
	for _, message := range messages[start:end] {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", message.ID, messagePreview(message.Content)))
	}
	return trimTrailingNewlines(sb.String()), nil
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

func formatSessions(sessions []storage.SessionSummary, currentID string) string {
	return formatSessionsWithOffset(sessions, currentID, 0)
}

func formatSessionsWithOffset(sessions []storage.SessionSummary, currentID string, numberOffset int) string {
	var sb strings.Builder
	sb.WriteString("sessions:\n")
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		sb.WriteString(fmt.Sprintf("  [%d] %s%s\n      id: %s\n      platform: %s/%s\n      updated: %s\n      messages: %d\n", numberOffset+i+1, title, sessionMarkers(s, currentID), s.ID, s.Platform, s.PlatformScopeID, formatTime(s.UpdatedAt), s.MessageCount))
		if s.MessagePreview != "" {
			sb.WriteString(fmt.Sprintf("      preview: %s\n", s.MessagePreview))
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
	modeModel := deps.Models.CurrentModeModel()
	compactModel := deps.Models.CurrentCompactModel()
	active := formatActiveRequests(ctx, deps, deps.Requests.List())
	return trimTrailingNewlines(fmt.Sprintf(`session status:
  current session: none
  default mode: %s
  current mode model: %s/%s
  compact model: %s/%s
  platform: %s/%s
%s  turn phase: %s
  pending input: %s
  tools: available in work mode
`, deps.Sessions.DefaultMode(), modeModel.Provider, modeModel.Model, compactModel.Provider, compactModel.Model, scope.Platform, scope.PlatformScopeID, indentStatusBlock(active), "idle", "none"))
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
		return "TODO"
	}
	return s
}
