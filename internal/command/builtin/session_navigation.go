package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	globalevents "elbot/internal/events"
)

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "sessions", Args: "[page] [keyword]", Description: "会话列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认1；keyword=搜索词，省略=不筛选",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, sessionPageSize(deps), false)
		if err != nil {
			return nil, err
		}
		deps.SessionState.set(deps.Scope(ctx), sessions)
		return sessionsResult(sessions, currentSessionID(ctx, deps), page, sessionPageSize(deps), query, hasNext, formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, deps.CommandText, "sessions")), nil
	})
}

func NewResume(deps Deps) command.Handler { return resumeCommand{deps: deps} }

type resumeCommand struct{ deps Deps }

func (c resumeCommand) Info() command.Info {
	return command.Info{
		Name: "resume", Args: "[number|session_id|--page <page>]", Description: "恢复会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser,
		Help: "省略=列可恢复会话；--page=翻页，page≥1\nnumber=该列表编号，更新倒序、跨页连续",
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
		return resumePage(ctx, deps, page)
	}
	if arg == "" {
		return resumePage(ctx, deps, 1)
	}
	sessionID := arg
	if idx, err := strconv.Atoi(arg); err == nil {
		if idx < 1 {
			return nil, inputError(fmt.Errorf("session index must be a positive number"))
		}
		sessions, err := deps.Sessions.ListResumablePage(ctx, deps.Scope(ctx), 1, idx-1)
		if err != nil {
			return nil, err
		}
		if len(sessions) == 0 {
			return nil, inputError(fmt.Errorf("session index %d out of range", idx))
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
		Content:      "title: " + session.Title,
		ResultStatus: globalevents.ResultSucceeded,
	}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	record.Identity.SessionID = session.ID
	globalevents.Log(ctx, record)
	return sessionChange(ctx, deps, session, session.ID, 1, formatResumeResult(ctx, deps, session)), nil
}

func resumePage(ctx context.Context, deps Deps, page int) (*command.Result, error) {
	pageSize := sessionPageSize(deps)
	sessions, hasNext, err := listResumablePage(ctx, deps, page, pageSize)
	if err != nil {
		return nil, err
	}
	return sessionsResult(sessions, currentSessionID(ctx, deps), page, pageSize, "", hasNext, formatResumableSessionsPage(sessions, page, pageSize, hasNext, deps.CommandText)), nil
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
	return command.NewFunc(command.Info{
		Name: "archives", Args: "[page] [keyword]", Description: "归档列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认1；keyword=搜索词，省略=不筛选",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, hasNext, err := listSessionPage(ctx, deps, query, page, sessionPageSize(deps), true)
		if err != nil {
			return nil, err
		}
		deps.SessionState.set(deps.Scope(ctx), sessions)
		return sessionsResult(sessions, currentSessionID(ctx, deps), page, sessionPageSize(deps), query, hasNext, formatSessionsPage(sessions, currentSessionID(ctx, deps), page, query, hasNext, deps.CommandText, "archives")), nil
	})
}

func NewMessages(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "messages", Args: "[page]", Description: "当前会话的用户与助手消息", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认最新页；页码越大越早，页内旧到新；bot 消息 ID 可用于 " + deps.CommandText("fork"),
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page := 1
		if arg := strings.TrimSpace(req.Args); arg != "" {
			parsed, err := strconv.Atoi(arg)
			if err != nil || parsed < 1 {
				return nil, inputError(fmt.Errorf("page must be a positive number"))
			}
			page = parsed
		}
		session, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
		if err != nil {
			return nil, err
		}

		loaded, err := (contextmgr.Loader{Store: deps.Store}).LoadMessagePage(ctx, session.ID, page, messageListPageSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, &command.Error{Kind: command.ErrorInternal, Code: "history_read_failed", Message: "读取会话历史失败", Cause: err}
		}
		if page > max(1, loaded.TotalPages) {
			return nil, inputError(fmt.Errorf("page %d out of range; available pages: 1-%d", page, max(1, loaded.TotalPages)))
		}
		items, err := messageData(loaded.Messages)
		if err != nil {
			return nil, err
		}
		var body strings.Builder
		fmt.Fprintf(&body, "page=%d/%d\n", page, loaded.TotalPages)
		for _, message := range loaded.Messages {
			if message.Role == "assistant" {
				fmt.Fprintf(&body, "bot: [%s] %s\n", message.ID, messagePreview(message.Content))
			} else {
				fmt.Fprintf(&body, "user: %s\n", messagePreview(message.Content))
			}
		}
		content := trimTrailingNewlines(body.String())
		if len(items) == 0 {
			content = "nil"
		}
		return &command.Result{Kind: command.ResultKindMessages, Text: content, Data: command.MessagesData{Page: command.Page{Page: page, PageSize: messageListPageSize, HasNext: page < loaded.TotalPages, TotalPages: &loaded.TotalPages}, SessionID: session.ID, Items: items}}, nil
	})
}

func NewFork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "fork", Args: "<message_id>", Description: "从消息创建并切换到分支", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser,
		Help: "message_id 见 " + deps.CommandText("messages") + "；原会话保留",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		messageID := strings.TrimSpace(req.Args)
		if messageID == "" {
			return nil, inputError(fmt.Errorf("usage: %s", deps.CommandText("fork", "<message_id>")))
		}
		session, err := deps.Sessions.Fork(ctx, deps.Scope(ctx), messageID)
		if err != nil {
			return nil, err
		}
		globalevents.Log(context.Background(), globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "session_fork",
			Module:       "command",
			Content:      fmt.Sprintf("parent: %s · message: %s", session.ParentSessionID, session.ForkFromMessageID),
			Identity:     globalevents.LogIdentity{SessionID: session.ID},
			ResultStatus: globalevents.ResultSucceeded,
		})
		return sessionChange(ctx, deps, session, session.ID, 1, formatForkResult(ctx, deps, session)), nil
	})
}
