package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextmgr"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

const resumeHistoryTurns = 3 // TODO: 后续支持配置恢复时展示多少轮历史。
const defaultSessionListPageSize = 10
const messageListPageSize = 20

func NewNew(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "new",
		Usage:       "/new",
		Description: "Create and switch to a new session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		session, err := deps.Sessions.Create(ctx, deps.Scope(ctx), "New session")
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("created new session:\n  id: %s\n  title: %s\n  mode: %s\n", session.ID, session.Title, session.Mode)}, nil
	})
}

func NewStatus(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "status",
		Usage:       "/status",
		Description: "Show current session status.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		status, err := deps.Sessions.Status(ctx, deps.Scope(ctx))
		if errors.Is(err, storage.ErrNotFound) {
			return &command.Result{Content: formatEmptyStatus(ctx, deps)}, nil
		}
		if err != nil {
			return nil, err
		}
		s := status.Session
		archived := "no"
		if s.ArchivedAt != nil {
			archived = formatTime(*s.ArchivedAt)
		}
		pinned := "no"
		if s.PinnedAt != nil {
			pinned = formatTime(*s.PinnedAt)
		}
		active := formatActiveRequests(deps.Requests.ListBySession(s.ID))
		turnSnapshot := deps.Turns.Snapshot(s.ID)
		pendingInput := "none"
		if turnSnapshot.PendingCount > 0 {
			pendingInput = fmt.Sprintf("%d message(s)", turnSnapshot.PendingCount)
		}
		toolUsage := formatToolUsage(ctx, deps, s.ID, turnSnapshot.Tools)
		currentModel := deps.Models.CurrentModelForMode(s.Mode)
		return &command.Result{Content: fmt.Sprintf(`session status:
  id: %s
  title: %s
  mode: %s
  current model: %s/%s
  message count: %d
  archived: %s
  pinned: %s
  created at: %s
  updated at: %s
  last ask: %s
  last answer: %s
%s  turn phase: %s
  pending input: %s
  tool usage: %s
%s`, s.ID, s.Title, s.Mode, currentModel.Provider, currentModel.Model, status.MessageCount, archived, pinned, formatTime(s.CreatedAt), formatTime(s.UpdatedAt), emptyTODO(status.LastUserPreview), emptyTODO(status.LastAnswerPreview), indentStatusBlock(active), turnSnapshot.Phase, pendingInput, toolUsage, deps.ContextStatus.ContextStatus(ctx, s))}, nil
	})
}

func formatToolUsage(ctx context.Context, deps Deps, sessionID string, live map[string]int) string {
	usage := map[string]int{}
	if deps.Store != nil && deps.Store.ToolCalls() != nil {
		summaries, err := deps.Store.ToolCalls().UsageBySession(ctx, sessionID)
		if err == nil {
			for _, summary := range summaries {
				if summary.ToolName != "" && summary.Count > 0 {
					usage[summary.ToolName] += summary.Count
				}
			}
		}
	}
	for name, count := range live {
		if count > 0 {
			usage[name] += count
		}
	}
	if out := turn.ToolsString(usage); out != "" {
		return out
	}
	return "none"
}

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "sessions",
		Usage:       "/sessions [keyword]",
		Description: "List or search sessions.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		pageSize := sessionPageSize(deps)
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, pageSize, false)
		if err != nil {
			return nil, err
		}
		currentID := currentSessionID(ctx, deps)
		deps.SetLastSessions(sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no sessions found\n"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentID, page, query, hasNext, "/sessions")}, nil
	})
}

func NewResume(deps Deps) command.Handler {
	return resumeCommand{deps: deps}
}

type resumeCommand struct {
	deps Deps
}

func (c resumeCommand) Info() command.Info {
	return command.Info{
		Name:        "resume",
		Usage:       "/resume [number|session_id]",
		Description: "Resume a previous session.",
	}
}

func (c resumeCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	if strings.HasPrefix(arg, "--page") {
		page, err := parseResumePageArg(arg)
		if err != nil {
			return nil, err
		}
		pageSize := sessionPageSize(deps)
		sessions, hasNext, err := listSessionPage(ctx, deps, "", page, pageSize, false)
		if err != nil {
			return nil, err
		}
		currentID := currentSessionID(ctx, deps)
		deps.SetLastSessions(sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no sessions found\n"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentID, page, "", hasNext, "/resume --page")}, nil
	}
	if arg == "" {
		pageSize := sessionPageSize(deps)
		sessions, hasNext, err := listSessionPage(ctx, deps, "", 1, pageSize, false)
		if err != nil {
			return nil, err
		}
		currentID := currentSessionID(ctx, deps)
		deps.SetLastSessions(sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no sessions found\n"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentID, 1, "", hasNext, "/resume --page")}, nil
	}

	sessionID := arg
	if idx, err := strconv.Atoi(arg); err == nil {
		ids := deps.LastSessions()
		if idx < 1 || idx > len(ids) {
			return nil, fmt.Errorf("session index %d out of range; run /sessions first", idx)
		}
		sessionID = ids[idx-1]
	}

	session, err := deps.Sessions.Resume(ctx, deps.Scope(ctx), sessionID)
	if err != nil {
		return nil, err
	}
	auditCommand(deps, "session_resume", "session_id", session.ID, "title", session.Title)
	return &command.Result{Content: formatResumeResult(ctx, deps, session)}, nil
}

func (c resumeCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	deps := c.deps
	if deps.Sessions == nil || deps.Scope == nil {
		return nil
	}
	cursor := req.Cursor
	if cursor <= 0 || cursor > len(req.Raw) {
		cursor = len(req.Raw)
	}
	tokenStart := cursor
	for tokenStart > 0 && req.Raw[tokenStart-1] != ' ' && req.Raw[tokenStart-1] != '\t' {
		tokenStart--
	}
	query := strings.TrimSpace(req.Raw[tokenStart:cursor])
	if query == "--page" || strings.HasPrefix(query, "--") {
		return nil
	}

	sessions, _, err := listSessionPage(ctx, deps, "", 1, sessionPageSize(deps), false)
	if err != nil {
		return nil
	}
	out := []command.Completion{}
	for _, session := range sessions {
		if query != "" && !strings.HasPrefix(session.ID, query) {
			continue
		}
		out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: tokenStart, ReplaceEnd: cursor})
	}
	return out
}

func NewArchives(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "archives",
		Usage:       "/archives [page] [keyword]",
		Description: "List archived sessions.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		pageSize := sessionPageSize(deps)
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, pageSize, true)
		if err != nil {
			return nil, err
		}
		currentID := currentSessionID(ctx, deps)
		deps.SetLastSessions(sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no archived sessions found\n"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentID, page, query, hasNext, "/archives")}, nil
	})
}

func NewArchive(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "archive",
		Usage:       "/archive [number|session_id] --confirm",
		Description: "Archive a session. Defaults to current session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		target, confirmed := parseTargetConfirm(req.Args)
		if !confirmed {
			content, err := confirmCommandMessage(ctx, deps, "archive", target)
			if err != nil {
				return nil, err
			}
			return &command.Result{Content: content}, nil
		}
		sessionID, err := resolveSessionTarget(ctx, deps, target)
		if err != nil {
			return nil, err
		}
		session, err := deps.Sessions.Archive(ctx, deps.Scope(ctx), sessionID)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("archived session:\n  id: %s\n  title: %s\n", session.ID, session.Title)}, nil
	})
}

func NewUnarchive(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "unarchive",
		Usage:       "/unarchive [number|session_id]",
		Description: "Unarchive a session. Defaults to current session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
		if err != nil {
			return nil, err
		}
		session, err := deps.Sessions.Unarchive(ctx, deps.Scope(ctx), sessionID)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("unarchived session:\n  id: %s\n  title: %s\n", session.ID, session.Title)}, nil
	})
}

func NewPin(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "pin",
		Usage:       "/pin [number|session_id]",
		Description: "Pin a session. Defaults to current session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
		if err != nil {
			return nil, err
		}
		session, err := deps.Sessions.Pin(ctx, deps.Scope(ctx), sessionID)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("pinned session:\n  id: %s\n  title: %s\n", session.ID, session.Title)}, nil
	})
}

func NewUnpin(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "unpin",
		Usage:       "/unpin [number|session_id]",
		Description: "Unpin a session. Defaults to current session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
		if err != nil {
			return nil, err
		}
		session, err := deps.Sessions.Unpin(ctx, deps.Scope(ctx), sessionID)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("unpinned session:\n  id: %s\n  title: %s\n", session.ID, session.Title)}, nil
	})
}

func NewRename(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "rename",
		Usage:       "/rename [number|session_id|current_title] <title>",
		Description: "Rename current or selected session.",
		Help:        "Usage:\n  /rename <title>\n  /rename <number|session_id|current_title> <title>\n\nIf current_title matches more than one visible session, use /sessions and rename by number or session id.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		sessionID, title, err := parseRenameArgs(ctx, deps, req.Args)
		if err != nil {
			return nil, err
		}
		session, err := deps.Sessions.Rename(ctx, deps.Scope(ctx), sessionID, title)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("renamed session:\n  id: %s\n  title: %s\n", session.ID, session.Title)}, nil
	})
}

func NewDelete(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "delete",
		Usage:       "/delete <number|session_id> --confirm",
		Description: "Delete a session permanently.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		target, confirmed := parseTargetConfirm(req.Args)
		if strings.TrimSpace(target) == "" {
			return nil, fmt.Errorf("usage: /delete <number|session_id> --confirm")
		}
		if !confirmed {
			content, err := confirmCommandMessage(ctx, deps, "delete", target)
			if err != nil {
				return nil, err
			}
			return &command.Result{Content: content}, nil
		}
		sessionID, err := resolveSessionTarget(ctx, deps, target)
		if err != nil {
			return nil, err
		}
		if err := deps.Sessions.Delete(ctx, deps.Scope(ctx), sessionID); err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("deleted session:\n  id: %s\n", sessionID)}, nil
	})
}

func NewClean(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "clean",
		Usage:       "/clean --confirm",
		Description: "Delete expired non-archived and non-pinned sessions.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		_, confirmed := parseTargetConfirm(req.Args)
		if !confirmed {
			return &command.Result{Content: "clean will permanently delete expired sessions that are not archived or pinned. Run /clean --confirm to continue.\n"}, nil
		}
		deleted, err := deps.Sessions.CleanupExpired(ctx, storage.Now().AddDate(0, 0, -cleanupRetentionDays(deps)))
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("cleaned expired sessions: %d\n", deleted)}, nil
	})
}

func NewMessages(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "messages",
		Usage:       "/messages [page]",
		Description: "List assistant message IDs in current session for fork.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page := 1
		arg := strings.TrimSpace(req.Args)
		if arg != "" {
			parsed, err := strconv.Atoi(arg)
			if err != nil || parsed < 1 {
				return nil, fmt.Errorf("page must be a positive number")
			}
			page = parsed
		}
		session, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
		if err != nil {
			return nil, err
		}
		loaded, err := (contextmgr.Loader{Store: deps.Store}).Load(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		assistants := assistantMessages(loaded.Messages)
		if len(assistants) == 0 {
			return &command.Result{Content: "no assistant messages found\n"}, nil
		}
		content, err := formatMessagePage(assistants, page, messageListPageSize)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: content}, nil
	})
}

func NewFork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "fork",
		Usage:       "/fork <message_id>",
		Description: "Fork current conversation from an assistant message.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		messageID := strings.TrimSpace(req.Args)
		if messageID == "" {
			return nil, fmt.Errorf("usage: /fork <message_id>")
		}
		// TODO: 平台适配层未来可把引用回复映射成内部 message_id 后复用本命令的 Service.Fork 流程。
		session, err := deps.Sessions.Fork(ctx, deps.Scope(ctx), messageID)
		if err != nil {
			return nil, err
		}
		auditCommand(deps, "session_fork", "session_id", session.ID, "parent_session_id", session.ParentSessionID, "from_message_id", session.ForkFromMessageID)
		return &command.Result{Content: formatForkResult(ctx, deps, session)}, nil
	})
}

func NewWork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "work",
		Usage:       "/work",
		Description: "Switch current session to work mode.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		scope := deps.Scope(ctx)
		session, err := deps.Sessions.Current(ctx, scope)
		if err != nil {
			if err == storage.ErrNotFound {
				session, err = deps.Sessions.CreateWithMode(ctx, scope, "New work session", storage.SessionModeWork)
			}
			if err != nil {
				return nil, err
			}
		} else {
			session, err = deps.Sessions.SetMode(ctx, scope, storage.SessionModeWork)
			if err != nil {
				return nil, err
			}
		}
		return &command.Result{Content: fmt.Sprintf("work mode active:\n  id: %s\n  mode: %s\n  tools: will apply from next message\n", session.ID, session.Mode)}, nil
	})
}

func NewChat(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "chat",
		Usage:       "/chat",
		Description: "Switch an empty session to chat mode, or create a chat session.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		scope := deps.Scope(ctx)
		session, err := deps.Sessions.Current(ctx, scope)
		if err != nil {
			if err == storage.ErrNotFound {
				session, err = deps.Sessions.CreateWithMode(ctx, scope, "New chat session", storage.SessionModeChat)
				if err != nil {
					return nil, err
				}
				return &command.Result{Content: fmt.Sprintf("chat mode active:\n  id: %s\n  mode: %s\n", session.ID, session.Mode)}, nil
			}
			return nil, err
		}
		if session.Mode == storage.SessionModeChat {
			return &command.Result{Content: fmt.Sprintf("already in chat mode:\n  id: %s\n", session.ID)}, nil
		}
		messages, err := deps.Store.Messages().ListBySession(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		if len(messages) > 0 {
			return &command.Result{Content: "current work session has history; run /new then /chat to start a clean chat session\n"}, nil
		}
		session, err = deps.Sessions.SetMode(ctx, scope, storage.SessionModeChat)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("chat mode active:\n  id: %s\n  mode: %s\n", session.ID, session.Mode)}, nil
	})
}

func auditCommand(deps Deps, event string, attrs ...any) {
	if deps.Audit == nil {
		return
	}
	deps.Audit(event, attrs...)
}

func cleanupRetentionDays(deps Deps) int {
	if deps.CleanupRetentionDays == nil {
		return 30
	}
	days := deps.CleanupRetentionDays()
	if days <= 0 {
		return 30
	}
	return days
}

func parseRenameArgs(ctx context.Context, deps Deps, args string) (string, string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("usage: /rename [number|session_id|current_title] <title>")
	}
	if len(fields) == 1 {
		return "", fields[0], nil
	}
	if sessionID, ok, err := resolveRenameTarget(ctx, deps, fields[0]); err != nil {
		return "", "", err
	} else if ok {
		return sessionID, strings.Join(fields[1:], " "), nil
	}
	return "", strings.Join(fields, " "), nil
}

func resolveRenameTarget(ctx context.Context, deps Deps, target string) (string, bool, error) {
	if idx, err := strconv.Atoi(target); err == nil {
		ids := deps.LastSessions()
		if idx < 1 || idx > len(ids) {
			return "", true, fmt.Errorf("session index %d out of range; run /sessions or /archives first", idx)
		}
		return ids[idx-1], true, nil
	}
	if deps.Store == nil {
		return "", false, nil
	}
	if session, err := deps.Store.Sessions().Get(ctx, target); err == nil {
		return session.ID, true, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return "", true, err
	}

	sessions, err := deps.Sessions.List(ctx, deps.Scope(ctx), target, sessionPageSize(deps))
	if err != nil {
		return "", true, err
	}
	matches := []string{}
	for _, session := range sessions {
		if session.Title == target {
			matches = append(matches, session.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0], true, nil
	default:
		return "", true, fmt.Errorf("session title %q matches multiple sessions; use /sessions and rename by number or session id", target)
	}
}

func parseSessionsArgs(args string) (int, string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return 1, "", nil
	}
	if page, err := strconv.Atoi(fields[0]); err == nil {
		if page < 1 {
			return 0, "", fmt.Errorf("page must be a positive number")
		}
		return page, strings.Join(fields[1:], " "), nil
	}
	return 1, strings.Join(fields, " "), nil
}

func parseResumePageArg(args string) (int, error) {
	fields := strings.Fields(args)
	if len(fields) != 2 || fields[0] != "--page" {
		return 0, fmt.Errorf("usage: /resume --page <page>")
	}
	page, err := strconv.Atoi(fields[1])
	if err != nil || page < 1 {
		return 0, fmt.Errorf("page must be a positive number")
	}
	return page, nil
}

func parseTargetConfirm(args string) (string, bool) {
	fields := strings.Fields(args)
	kept := make([]string, 0, len(fields))
	confirmed := false
	for _, field := range fields {
		if field == "--confirm" {
			confirmed = true
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(kept, " "), confirmed
}

func confirmCommandMessage(ctx context.Context, deps Deps, name, target string) (string, error) {
	sessionID, err := resolveSessionTarget(ctx, deps, target)
	if err != nil {
		return "", err
	}
	session, err := deps.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	confirmTarget := strings.TrimSpace(target)
	if confirmTarget == "" {
		return fmt.Sprintf("%s will modify current session:\n  title: %s\n  id: %s\nRun /%s --confirm to continue.\n", name, emptyTitle(session.Title), session.ID, name), nil
	}
	return fmt.Sprintf("%s will modify session:\n  title: %s\n  id: %s\nRun /%s %s --confirm to continue.\n", name, emptyTitle(session.Title), session.ID, name, confirmTarget), nil
}

func emptyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "(untitled)"
	}
	return title
}

func resolveSessionTarget(ctx context.Context, deps Deps, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		current, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
		if err != nil {
			return "", err
		}
		return current.ID, nil
	}
	if idx, err := strconv.Atoi(target); err == nil {
		ids := deps.LastSessions()
		if idx < 1 || idx > len(ids) {
			return "", fmt.Errorf("session index %d out of range; run /sessions or /archives first", idx)
		}
		return ids[idx-1], nil
	}
	return target, nil
}

func sessionPageSize(deps Deps) int {
	if deps.SessionListPageSize == nil {
		return defaultSessionListPageSize
	}
	pageSize := deps.SessionListPageSize()
	if pageSize <= 0 {
		return defaultSessionListPageSize
	}
	return pageSize
}

func listSessionPage(ctx context.Context, deps Deps, query string, page, pageSize int, archivedOnly bool) ([]storage.SessionSummary, bool, error) {
	if page < 1 {
		return nil, false, fmt.Errorf("page must be a positive number")
	}
	if pageSize <= 0 {
		pageSize = defaultSessionListPageSize
	}
	limit := pageSize + 1
	offset := (page - 1) * pageSize
	sessions, err := deps.Sessions.ListPage(ctx, deps.Scope(ctx), query, limit, offset, archivedOnly)
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
	content := formatSessions(sessions, currentID)
	var sb strings.Builder
	sb.WriteString(content)
	sb.WriteString(fmt.Sprintf("page: %d\n", page))
	if page > 1 {
		sb.WriteString(fmt.Sprintf("prev: %s\n", nextPageCommand(commandPrefix, page-1, query)))
	}
	if hasNext {
		sb.WriteString(fmt.Sprintf("next: %s\n", nextPageCommand(commandPrefix, page+1, query)))
	}
	return sb.String()
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
	return sb.String()
}

func formatForkResult(ctx context.Context, deps Deps, session *storage.Session) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("forked session:\n  id: %s\n  parent: %s\n  from message: %s\n  mode: %s\n", session.ID, session.ParentSessionID, session.ForkFromMessageID, session.Mode))
	appendRecentMessages(&sb, ctx, deps, session.ID)
	return sb.String()
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
	messages := loaded.Messages
	limit := turns * 2
	out := make([]storage.Message, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		message := messages[i]
		if message.Role != storage.RoleUser && message.Role != storage.RoleAssistant {
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		out = append(out, message)
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
		if message.Role != storage.RoleAssistant || strings.TrimSpace(message.Content) == "" {
			continue
		}
		out = append(out, message)
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
	end := start + pageSize
	if end > len(messages) {
		end = len(messages)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("messages page %d/%d:\n", page, totalPages))
	for _, message := range messages[start:end] {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", message.ID, messagePreview(message.Content)))
	}
	return sb.String(), nil
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
	var sb strings.Builder
	sb.WriteString("sessions:\n")
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		markers := sessionMarkers(s, currentID)
		sb.WriteString(fmt.Sprintf("  [%d] %s%s\n      id: %s\n      platform: %s/%s\n      updated: %s\n      messages: %d\n", i+1, title, markers, s.ID, s.Platform, s.PlatformScopeID, formatTime(s.UpdatedAt), s.MessageCount))
		if s.MessagePreview != "" {
			sb.WriteString(fmt.Sprintf("      preview: %s\n", s.MessagePreview))
		}
	}
	return sb.String()
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
	_ = ctx
	scope := deps.Scope(ctx)
	modeModel := deps.Models.CurrentModeModel()
	compactModel := deps.Models.CurrentCompactModel()
	active := formatActiveRequests(deps.Requests.List())
	return fmt.Sprintf(`session status:
  current session: none
  default mode: %s
  current mode model: %s/%s
  compact model: %s/%s
  platform: %s/%s
%s  turn phase: %s
  pending input: %s
  tools: available in work mode
`, deps.Sessions.DefaultMode(), modeModel.Provider, modeModel.Model, compactModel.Provider, compactModel.Model, scope.Platform, scope.PlatformScopeID, indentStatusBlock(active), "idle", "none")
}

func indentStatusBlock(text string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	for _, line := range lines {
		if line == "" {
			continue
		}
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

func formatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func emptyTODO(s string) string {
	if s == "" {
		return "TODO"
	}
	return s
}

type SessionCoreModule struct{}

func (SessionCoreModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewNew,
		NewStatus,
	)
}

type SessionListModule struct{}

func (SessionListModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewSessions,
		NewArchives,
	)
}

type SessionLifecycleModule struct{}

func (SessionLifecycleModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewArchive,
		NewUnarchive,
		NewPin,
		NewUnpin,
		NewRename,
		NewDelete,
		NewClean,
	)
}

type SessionResumeModule struct{}

func (SessionResumeModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewResume)
}

type SessionForkModule struct{}

func (SessionForkModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewMessages,
		NewFork,
	)
}

type SessionModeModule struct{}

func (SessionModeModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewWork,
		NewChat,
	)
}

type SessionModule struct{}

func (SessionModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterModules(registrar, deps,
		SessionCoreModule{},
		SessionListModule{},
		SessionLifecycleModule{},
		SessionResumeModule{},
		SessionForkModule{},
		SessionModeModule{},
	)
}
