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
	"elbot/internal/storage"
)

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "sessions", Args: "[page] [-k|--keyword <keyword>]", Description: "会话列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认第一页；前翻=页码-1\n置顶优先，其余按更新时间倒序；-k/--keyword 后为搜索词",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, err := listSessionPage(ctx, deps, query, page, false, false)
		if err != nil {
			return nil, err
		}
		return sessionsResult(ctx, deps, sessions, query, "sessions")
	})
}

func NewResume(deps Deps) command.Handler { return resumeCommand{deps: deps} }

type resumeCommand struct{ deps Deps }

func (c resumeCommand) Info() command.Info {
	return command.Info{
		Name: "resume", Args: "[session_id|last|--page <page>]", Description: "恢复会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser,
		Help: "省略=列表；默认第一页；--page≥1，前翻=页码-1\nlast=最近更新，排除当前，忽略置顶",
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
		return resumePage(ctx, deps, 0)
	}
	var session *storage.Session
	var err error
	if arg == "last" {
		session, err = deps.Sessions.ResumeLast(ctx, deps.Scope(ctx))
	} else {
		session, err = deps.Sessions.Resume(ctx, deps.Scope(ctx), arg)
	}
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
	history, historyErr := recentConversationMessages(ctx, deps, session.ID, resumeHistoryTurns)
	result := sessionChangeAtCurrent(session, session.ID, 1, session.ID, "resume")
	data := result.Data.(command.SessionChangeData)
	data.Messages, err = messageData(history)
	if err != nil {
		return result, err
	}
	result.Data = data
	return result, historyErr
}

func resumePage(ctx context.Context, deps Deps, page int) (*command.Result, error) {
	sessions, err := listSessionPage(ctx, deps, "", page, false, true)
	if err != nil {
		return nil, err
	}
	return sessionsResult(ctx, deps, sessions, "", "resume")
}

func (c resumeCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	query := strings.TrimSpace(token.Text)
	if query == "--page" || strings.HasPrefix(query, "--") {
		return nil
	}
	sessions, err := listSessionPage(ctx, c.deps, "", 0, false, true)
	if err != nil {
		return nil
	}
	out := completeStringOptions([]string{"last"}, query, token.Start, token.End, "keyword")
	for _, session := range sessions.Items {
		if query == "" || strings.HasPrefix(session.ID, query) {
			out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: token.Start, ReplaceEnd: token.End})
		}
	}
	return out
}

func NewArchives(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "archives", Args: "[page] [-k|--keyword <keyword>]", Description: "归档列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认第一页；前翻=页码-1\n置顶优先，其余按更新时间倒序；-k/--keyword 后为搜索词",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, err := listSessionPage(ctx, deps, query, page, true, false)
		if err != nil {
			return nil, err
		}
		return sessionsResult(ctx, deps, sessions, query, "archives")
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
		return &command.Result{Kind: command.ResultKindMessages, Data: command.MessagesData{Page: command.Page{Page: page, PageSize: messageListPageSize, HasNext: page < loaded.TotalPages, TotalPages: &loaded.TotalPages}, SessionID: session.ID, Items: items}}, nil
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
		history, historyErr := recentConversationMessages(ctx, deps, session.ID, resumeHistoryTurns)
		result := sessionChangeAtCurrent(session, session.ID, 1, session.ID, "fork")
		data := result.Data.(command.SessionChangeData)
		data.Messages, err = messageData(history)
		if err != nil {
			return result, err
		}
		result.Data = data
		return result, historyErr
	})
}
