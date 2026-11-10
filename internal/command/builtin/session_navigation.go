package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
)

func NewSessions(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "sessions", Args: "[page] [-k|--keyword <keyword>] [-a|--all]", Description: "会话列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认第一页；前翻=页码-1\n置顶优先，其余按更新时间倒序；-k/--keyword 后为搜索词；-a/--all 查看所有平台（仅超管）",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, all, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, err := listSessionPage(ctx, deps, query, page, false, false, all)
		if err != nil {
			return nil, err
		}
		return sessionsResult(sessions), nil
	})
}

func NewResume(deps Deps) command.Handler { return resumeCommand{deps: deps} }

type resumeCommand struct{ deps Deps }

func (c resumeCommand) Info() command.Info {
	return command.Info{
		Name: "resume", Args: "[session_id|last|--page <page> [-a|--all]]", Description: "恢复会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser,
		Help: "省略=列表；默认第一页；--page≥1，前翻=页码-1\nlast=最近更新，排除当前，忽略置顶；列表或 last 可加 -a/--all（仅超管）",
	}
}

func (c resumeCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	page, all, target, pageSet, err := parseResumeArgs(arg)
	if err != nil {
		return nil, err
	}
	if pageSet {
		return resumePage(ctx, deps, page, all)
	}
	if target == "" {
		return resumePage(ctx, deps, 0, all)
	}
	if all && target != "last" {
		return nil, inputError(fmt.Errorf("-a/--all is only valid for the resume list or last"))
	}
	data, err := deps.Control.ResumeSession(ctx, control.ResumeSessionRequest{SessionID: target, MessageLimit: resumeHistoryTurns * 2, AllPlatforms: all})
	return sessionChange(data), err
}

func resumePage(ctx context.Context, deps Deps, page int, all bool) (*command.Result, error) {
	sessions, err := listSessionPage(ctx, deps, "", page, false, true, all)
	if err != nil {
		return nil, err
	}
	return sessionsResult(sessions), nil
}

func (c resumeCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	query := strings.TrimSpace(token.Text)
	if query == "--page" || strings.HasPrefix(query, "--") || query == "-a" {
		return command.CompleteStaticOptions([]command.CompletionOption{{Text: "--all", Description: "查看所有平台会话（仅超管）"}}, query, token.Start, token.End, "option")
	}
	all := hasAllPlatformsFlag(req.Args)
	sessions, err := listSessionPage(ctx, c.deps, "", 0, false, true, all)
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
		Name: "archives", Args: "[page] [-k|--keyword <keyword>] [-a|--all]", Description: "归档列表/搜索", MinRole: contextinfo.RoleUser,
		Help: "page≥1，默认第一页；前翻=页码-1\n置顶优先，其余按更新时间倒序；-k/--keyword 后为搜索词；-a/--all 查看所有平台（仅超管）",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		page, query, all, err := parseSessionsArgs(req.Args)
		if err != nil {
			return nil, err
		}
		sessions, err := listSessionPage(ctx, deps, query, page, true, false, all)
		if err != nil {
			return nil, err
		}
		return sessionsResult(sessions), nil
	})
}

func parseResumeArgs(args string) (page int, all bool, target string, pageSet bool, err error) {
	fields := strings.Fields(args)
	kept := fields[:0]
	for _, field := range fields {
		switch field {
		case "-a", "--all":
			if all {
				return 0, false, "", false, inputError(fmt.Errorf("duplicate -a/--all"))
			}
			all = true
		default:
			kept = append(kept, field)
		}
	}
	if len(kept) == 0 {
		return 0, all, "", false, nil
	}
	if kept[0] == "--page" {
		if len(kept) < 2 {
			return 0, false, "", false, inputError(fmt.Errorf("expected --page <page>"))
		}
		page, err = parseResumePageArg(strings.Join(kept, " "))
		if err != nil {
			return 0, false, "", false, err
		}
		return page, all, "", true, nil
	}
	if strings.HasPrefix(kept[0], "--page") {
		return 0, false, "", false, inputError(fmt.Errorf("expected --page <page>"))
	}
	if len(kept) != 1 {
		return 0, false, "", false, inputError(fmt.Errorf("expected one session ID, last, or --page <page>"))
	}
	if all && kept[0] != "last" {
		return 0, false, "", false, inputError(fmt.Errorf("-a/--all is only valid for the resume list or last"))
	}
	return 0, all, kept[0], false, nil
}

func hasAllPlatformsFlag(args string) bool {
	for _, field := range strings.Fields(args) {
		if field == "-a" || field == "--all" {
			return true
		}
	}
	return false
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
		data, err := deps.Control.SessionMessages(ctx, control.SessionMessagesRequest{Page: page, PageSize: messageListPageSize})
		return &command.Result{Kind: command.ResultKindMessages, Data: data}, err
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
		data, err := deps.Control.ForkSession(ctx, control.ForkSessionRequest{MessageID: messageID, MessageLimit: resumeHistoryTurns * 2})
		return sessionChange(data), err
	})
}
