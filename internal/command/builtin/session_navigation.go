package builtin

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	globalevents "elbot/internal/events"
)

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "sessions", Usage: "/sessions [keyword]", Description: "会话列表/搜索", MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
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
			return &command.Result{Content: "nil"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, "/sessions")}, nil
	})
}

func NewResume(deps Deps) command.Handler { return resumeCommand{deps: deps} }

type resumeCommand struct{ deps Deps }

func (c resumeCommand) Info() command.Info {
	return command.Info{Name: "resume", Usage: "/resume [number|session_id]", Description: "恢复会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser}
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
	record := globalevents.LogRecord{
		Category:     globalevents.LogAudit,
		Level:        slog.LevelInfo,
		EventName:    "session_resume",
		Module:       "command",
		Content:      "session resumed: title=" + session.Title,
		Identity:     globalevents.LogIdentity{SessionID: &session.ID},
		ResultStatus: globalevents.ResultSucceeded,
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	_ = globalevents.EmitLog(ctx, record)
	return &command.Result{Content: formatResumeResult(ctx, deps, session)}, nil
}

func resumePage(ctx context.Context, deps Deps, page int) (*command.Result, error) {
	pageSize := sessionPageSize(deps)
	sessions, hasNext, err := listResumablePage(ctx, deps, page, pageSize)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return &command.Result{Content: "nil"}, nil
	}
	return &command.Result{Content: formatResumableSessionsPage(sessions, page, pageSize, hasNext)}, nil
}

func (c resumeCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	query := strings.TrimSpace(token.Text)
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
			out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: token.Start, ReplaceEnd: token.End})
		}
	}
	return out
}

func NewArchives(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "archives", Usage: "/archives [page] [keyword]", Description: "归档列表", MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
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
			return &command.Result{Content: "nil"}, nil
		}
		return &command.Result{Content: formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, "/archives")}, nil
	})
}

func NewMessages(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "messages", Usage: "/messages [page]", Description: "消息 ID 列表", MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
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
			return &command.Result{Content: "nil"}, nil
		}
		content, err := formatMessagePage(assistants, page, messageListPageSize)
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: content}, nil
	})
}

func NewFork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "fork", Usage: "/fork <message_id>", Description: "从消息创建分支", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		messageID := strings.TrimSpace(req.Args)
		if messageID == "" {
			return nil, fmt.Errorf("usage: /fork <message_id>")
		}
		session, err := deps.Sessions.Fork(ctx, deps.Scope(ctx), messageID)
		if err != nil {
			return nil, err
		}
		_ = globalevents.EmitLog(context.Background(), globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "session_fork",
			Module:       "command",
			Content:      fmt.Sprintf("session forked: parent_session_id=%s from_message_id=%s", session.ParentSessionID, session.ForkFromMessageID),
			Identity:     globalevents.LogIdentity{SessionID: &session.ID},
			ResultStatus: globalevents.ResultSucceeded,
		})
		return &command.Result{Content: formatForkResult(ctx, deps, session)}, nil
	})
}
