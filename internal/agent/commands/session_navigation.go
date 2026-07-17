package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextmgr"
	"elbot/internal/security"
)

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "sessions", Usage: "/sessions [keyword]", Description: "List or search sessions.", MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, sessionPageSize(deps), false)
		if err != nil {
			return nil, err
		}
		deps.SessionState.set(deps.Scope(ctx), sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no sessions found"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, "/sessions")}, nil
	})
}

func NewResume(deps Deps) command.Handler { return resumeCommand{deps: deps} }

type resumeCommand struct{ deps Deps }

func (c resumeCommand) Info() command.Info {
	return command.Info{Name: "resume", Usage: "/resume [number|session_id]", Description: "Resume a previous session.", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: security.RoleUser}
}

func (c resumeCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	if strings.HasPrefix(arg, "--page") {
		page, err := parseResumePageArg(arg)
		if err != nil {
			return nil, err
		}
		return resumePage(ctx, deps, page)
	}
	if arg == "" {
		return resumePage(ctx, deps, 1)
	}
	sessionID := arg
	if idx, err := strconv.Atoi(arg); err == nil {
		if idx < 1 {
			return nil, fmt.Errorf("session index must be a positive number")
		}
		sessions, err := deps.Sessions.ListResumablePage(ctx, deps.Scope(ctx), 1, idx-1)
		if err != nil {
			return nil, err
		}
		if len(sessions) == 0 {
			return nil, fmt.Errorf("session index %d out of range", idx)
		}
		sessionID = sessions[0].ID
	}
	session, err := deps.Sessions.Resume(ctx, deps.Scope(ctx), sessionID)
	if err != nil {
		return nil, err
	}
	auditCommand(deps, "session_resume", "session_id", session.ID, "title", session.Title)
	return &command.Result{Content: formatResumeResult(ctx, deps, session)}, nil
}

func resumePage(ctx context.Context, deps Deps, page int) (*command.Result, error) {
	pageSize := sessionPageSize(deps)
	sessions, hasNext, err := listResumablePage(ctx, deps, page, pageSize)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return &command.Result{Content: "no sessions found"}, nil
	}
	return &command.Result{Content: formatResumableSessionsPage(sessions, page, pageSize, hasNext)}, nil
}

func (c resumeCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
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
	sessions, _, err := listResumablePage(ctx, c.deps, 1, sessionPageSize(c.deps))
	if err != nil {
		return nil
	}
	out := []command.Completion{}
	for _, session := range sessions {
		if query == "" || strings.HasPrefix(session.ID, query) {
			out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: tokenStart, ReplaceEnd: cursor})
		}
	}
	return out
}

func NewArchives(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "archives", Usage: "/archives [page] [keyword]", Description: "List archived sessions.", MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, sessionPageSize(deps), true)
		if err != nil {
			return nil, err
		}
		deps.SessionState.set(deps.Scope(ctx), sessions)
		if len(sessions) == 0 {
			return &command.Result{Content: "no archived sessions found"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, "/archives")}, nil
	})
}

func NewMessages(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "messages", Usage: "/messages [page]", Description: "List assistant message IDs in current session for fork.", MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page := 1
		if arg := strings.TrimSpace(req.Args); arg != "" {
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
			return &command.Result{Content: "no assistant messages found"}, nil
		}
		content, err := formatMessagePage(assistants, page, messageListPageSize)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: content}, nil
	})
}

func NewFork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "fork", Usage: "/fork <message_id>", Description: "Fork current conversation from an assistant message.", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		messageID := strings.TrimSpace(req.Args)
		if messageID == "" {
			return nil, fmt.Errorf("usage: /fork <message_id>")
		}
		session, err := deps.Sessions.Fork(ctx, deps.Scope(ctx), messageID)
		if err != nil {
			return nil, err
		}
		auditCommand(deps, "session_fork", "session_id", session.ID, "parent_session_id", session.ParentSessionID, "from_message_id", session.ForkFromMessageID)
		return &command.Result{Content: formatForkResult(ctx, deps, session)}, nil
	})
}
