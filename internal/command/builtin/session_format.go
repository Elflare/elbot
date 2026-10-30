package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/storage"
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

func formatResumeResult(ctx context.Context, deps Deps, session *storage.Session) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("resume → %s · %s\n", session.Title, session.ID))
	appendRecentMessages(&sb, ctx, deps, session.ID)
	return trimTrailingNewlines(sb.String())
}

func formatForkResult(ctx context.Context, deps Deps, session *storage.Session) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fork → %s · %s\nparent=%s · from=%s\n", session.ID, session.Mode, session.ParentSessionID, session.ForkFromMessageID))
	appendRecentMessages(&sb, ctx, deps, session.ID)
	return trimTrailingNewlines(sb.String())
}

func appendRecentMessages(sb *strings.Builder, ctx context.Context, deps Deps, sessionID string) {
	history := recentConversationMessages(ctx, deps, sessionID, resumeHistoryTurns)
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
	return truncateRunes(strings.TrimSpace(strings.ReplaceAll(text, "\n", " ")), 80)
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
	sb.WriteString(fmt.Sprintf("page=%d/%d\n", page, totalPages))
	for _, message := range messages[start:end] {
		sb.WriteString(fmt.Sprintf("%s: %s\n", message.ID, messagePreview(message.Content)))
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
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("[%d] %s%s\n%s · %s/%s · %s · msgs=%d\n", numberOffset+i+1, title, sessionMarkers(s, currentID), s.ID, s.Platform, s.PlatformScopeID, formatTime(s.UpdatedAt), s.MessageCount))
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
	active := formatActiveRequests(ctx, deps, nil)
	return trimTrailingNewlines(fmt.Sprintf(`session=nil
default=%s · model=%s/%s
compact.model=%s/%s
platform=%s/%s
%sphase=idle · pending=0
`, deps.Sessions.DefaultMode(), modeModel.Provider, modeModel.Model, compactModel.Provider, compactModel.Model, scope.Platform, scope.PlatformScopeID, indentStatusBlock(active)))
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
