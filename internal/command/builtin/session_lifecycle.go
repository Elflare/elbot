package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

const resumeHistoryTurns = 3
const messageListPageSize = 20

func NewArchive(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "archive",
		args:        "[session_id] [--confirm]",
		description: "归档会话",
		help:        "省略目标=当前",
		confirm:     true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (command.Result, error) {
			target, confirmed := parseTargetConfirm(req.Args)
			if !confirmed {
				content, err := confirmCommandMessage(ctx, deps, "archive", target)
				if err != nil {
					return command.Result{}, err
				}
				return content, nil
			}
			session, err := deps.Control.SetSessionArchived(ctx, control.SetSessionArchivedRequest{SessionID: target, Archived: true, Confirmed: true})
			if err != nil {
				return command.Result{}, err
			}
			return sessionChange(session), nil
		},
	}
}

func NewUnarchive(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "unarchive",
		args:        "[session_id]",
		description: "取消归档",
		help:        "省略目标=当前",
		archived:    true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (command.Result, error) {
			session, err := deps.Control.SetSessionArchived(ctx, control.SetSessionArchivedRequest{SessionID: req.Args, Archived: false})
			if err != nil {
				return command.Result{}, err
			}
			return sessionChange(session), nil
		},
	}
}

func NewPin(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "pin",
		args:        "[session_id]",
		description: "置顶会话",
		help:        "省略目标=当前",
		handle: func(ctx context.Context, deps Deps, req command.Request) (command.Result, error) {
			session, err := deps.Control.SetSessionPinned(ctx, control.SetSessionPinnedRequest{SessionID: req.Args, Pinned: true})
			if err != nil {
				return command.Result{}, err
			}
			return sessionChange(session), nil
		},
	}
}

func NewUnpin(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "unpin",
		args:        "[session_id]",
		description: "取消置顶",
		help:        "省略目标=当前",
		handle: func(ctx context.Context, deps Deps, req command.Request) (command.Result, error) {
			session, err := deps.Control.SetSessionPinned(ctx, control.SetSessionPinnedRequest{SessionID: req.Args, Pinned: false})
			if err != nil {
				return command.Result{}, err
			}
			return sessionChange(session), nil
		},
	}
}

func NewRename(deps Deps) command.Handler {
	return renameCommand{deps: deps}
}

type renameCommand struct {
	deps Deps
}

func (c renameCommand) Info() command.Info {
	return command.Info{
		Name:          "rename",
		Args:          "[session_id|current_title] <title>",
		Description:   "重命名会话",
		SessionEffect: command.SessionEffectMutate,
		MinRole:       contextinfo.RoleUser,
		Help:          "省略目标=当前",
	}
}

func (c renameCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	target, title, ok := parseRenameArgs(req.Args)
	if !ok {
		return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", c.deps.CommandText("rename", "[session_id|current_title]", "<title>"))}, nil
	}
	session, err := c.deps.Control.RenameSession(ctx, control.RenameSessionRequest{Target: target, Title: title, PageSize: c.deps.SessionListPageSize})
	if err != nil {
		return command.Result{}, err
	}
	return sessionChange(session), nil
}

func (c renameCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	return completeSessionIDs(ctx, c.deps, token.Text, false, token.Start, token.End)
}

func NewDelete(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "delete",
		args:        "<session_id> [--confirm]",
		description: "永久删除会话",
		confirm:     true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (command.Result, error) {
			target, confirmed := parseTargetConfirm(req.Args)
			if target == "" {
				return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", deps.CommandText("delete", "<session_id>", "--confirm"))}, nil
			}
			if !confirmed {
				content, err := confirmCommandMessage(ctx, deps, "delete", target)
				if err != nil {
					return command.Result{}, err
				}
				return content, nil
			}
			data, err := deps.Control.DeleteSession(ctx, control.DeleteSessionRequest{SessionID: target, Confirmed: true})
			if err != nil {
				return command.Result{}, err
			}
			return sessionChange(data), nil
		},
	}
}

func NewClean(deps Deps) command.Handler {
	return cleanCommand{deps: deps}
}

type cleanCommand struct {
	deps Deps
}

func (c cleanCommand) Info() command.Info {
	return command.Info{
		Name:          "clean",
		Args:          "[--confirm]",
		Description:   "永久清理过期会话",
		SessionEffect: command.SessionEffectMutate,
		Help:          "仅清理未归档、未置顶的过期会话",
	}
}

func (c cleanCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	_, confirmed := parseTargetConfirm(req.Args)
	if !confirmed {
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindSession, Data: control.CleanSessionsResult{}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认清理", Command: c.deps.CommandText("clean", "--confirm")}}}}, nil
	}
	data, err := c.deps.Control.CleanSessions(ctx, control.CleanSessionsRequest{Confirmed: true, Before: storage.Now().AddDate(0, 0, -cleanupRetentionDays(c.deps))})
	if err != nil {
		return command.Result{}, err
	}
	return sessionChange(data), nil
}

func (c cleanCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	return completeConfirmFlag(req.Args, token)
}

type sessionTargetCommand struct {
	deps        Deps
	name        string
	args        string
	description string
	help        string // 命令的参数说明与必要规则，传入 Info.Help。
	archived    bool
	confirm     bool
	handle      func(context.Context, Deps, command.Request) (command.Result, error)
}

func (c sessionTargetCommand) Info() command.Info {
	return command.Info{Name: c.name, Args: c.args, Description: c.description, Help: c.help, SessionEffect: command.SessionEffectMutate, MinRole: contextinfo.RoleUser}
}

func (c sessionTargetCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	return c.handle(ctx, c.deps, req)
}

func (c sessionTargetCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	fields := strings.Fields(req.Args)
	if len(fields) > 0 && fields[len(fields)-1] == "--confirm" {
		return nil
	}
	if c.confirm && len(fields) > 0 && (!command.IsFirstArg(req, token) || strings.HasPrefix(token.Text, "--")) {
		return completeConfirmFlag(req.Args, token)
	}
	if command.IsFirstArg(req, token) {
		return completeSessionIDs(ctx, c.deps, token.Text, c.archived, token.Start, token.End)
	}
	if c.confirm {
		return completeConfirmFlag(req.Args, token)
	}
	return nil
}

func cleanupRetentionDays(deps Deps) int {
	days := deps.SessionRetentionDays
	if days <= 0 {
		return 30
	}
	return days
}

func parseRenameArgs(args string) (string, string, bool) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", "", false
	}
	if len(fields) == 1 {
		return "", fields[0], true
	}
	return fields[0], strings.Join(fields[1:], " "), true
}

func parseSessionsArgs(args string) (int, string, bool, error) {
	fields := strings.Fields(args)
	kept := fields[:0]
	all := false
	for _, field := range fields {
		if field == "-a" || field == "--all" {
			if all {
				return 0, "", false, fmt.Errorf("duplicate -a/--all")
			}
			all = true
			continue
		}
		kept = append(kept, field)
	}
	fields = kept
	if len(fields) == 0 {
		return 0, "", all, nil
	}
	page := 0
	if fields[0] != "-k" && fields[0] != "--keyword" {
		var err error
		page, err = strconv.Atoi(fields[0])
		if err != nil || page < 1 {
			return 0, "", false, fmt.Errorf("expected page≥1 or -k/--keyword")
		}
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return page, "", all, nil
	}
	if (fields[0] != "-k" && fields[0] != "--keyword") || len(fields) < 2 {
		return 0, "", false, fmt.Errorf("expected -k/--keyword <keyword>")
	}
	return page, strings.Join(fields[1:], " "), all, nil
}

func parseResumePageArg(args string) (int, error) {
	fields := strings.Fields(args)
	if len(fields) != 2 || fields[0] != "--page" {
		return 0, fmt.Errorf("expected --page <page>")
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

func confirmCommandMessage(ctx context.Context, deps Deps, name, target string) (command.Result, error) {
	row, err := deps.Control.GetSession(ctx, control.GetSessionRequest{SessionID: target})
	if err != nil {
		return command.Result{}, err
	}
	if row.Status != operationresult.Success {
		return command.Result{Status: row.Status, Message: row.Message}, nil
	}
	data := row.Data
	action := deps.CommandText(name, data.ID, "--confirm")
	return command.Result{Status: operationresult.Success, Kind: command.ResultKindSession, Data: control.SessionChangeResult{Session: &data, SessionID: data.ID}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认", Command: action}}}}, nil
}

func emptyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "(untitled)"
	}
	return title
}
