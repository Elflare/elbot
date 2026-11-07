package builtin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const resumeHistoryTurns = 3 // TODO: 后续支持配置恢复时展示多少轮历史。
const defaultSessionListPageSize = 10
const messageListPageSize = 20

func NewArchive(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "archive",
		args:        "[session_id] [--confirm]",
		description: "归档会话",
		help:        "省略目标=当前",
		confirm:     true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			target, confirmed := parseTargetConfirm(req.Args)
			if !confirmed {
				content, err := confirmCommandMessage(ctx, deps, "archive", target)
				if err != nil {
					return nil, err
				}
				return content, nil
			}
			sessionID, err := resolveSessionTarget(ctx, deps, target)
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Archive(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return sessionChange(ctx, deps, session, session.ID, 1, "archive"), nil
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
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, req.Args)
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Unarchive(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return sessionChange(ctx, deps, session, session.ID, 1, "unarchive"), nil
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
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, req.Args)
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Pin(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return sessionChange(ctx, deps, session, session.ID, 1, "pin"), nil
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
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, req.Args)
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Unpin(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return sessionChange(ctx, deps, session, session.ID, 1, "unpin"), nil
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

func (c renameCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	sessionID, title, err := parseRenameArgs(ctx, deps, req.Args)
	if err != nil {
		return nil, err
	}
	session, err := deps.Sessions.Rename(ctx, deps.Scope(ctx), sessionID, title)
	if err != nil {
		return nil, err
	}
	return sessionChange(ctx, deps, session, session.ID, 1, "rename"), nil
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
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			target, confirmed := parseTargetConfirm(req.Args)
			if target == "" {
				return nil, inputError(fmt.Errorf("usage: %s", deps.CommandText("delete", "<session_id>", "--confirm")))
			}
			if !confirmed {
				content, err := confirmCommandMessage(ctx, deps, "delete", target)
				if err != nil {
					return nil, err
				}
				return content, nil
			}
			sessionID, err := resolveSessionTarget(ctx, deps, target)
			if err != nil {
				return nil, err
			}
			if err := deps.Sessions.Delete(ctx, deps.Scope(ctx), sessionID); err != nil {
				return nil, err
			}
			return sessionChange(ctx, deps, nil, sessionID, 1, "delete"), nil
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

func (c cleanCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	_, confirmed := parseTargetConfirm(req.Args)
	if !confirmed {
		return &command.Result{Kind: command.ResultKindSession, Data: command.SessionChangeData{Operation: "clean", Prompt: "永久删除过期且未归档、未置顶的会话？\n" + deps.CommandText("clean", "--confirm")}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认清理", Command: deps.CommandText("clean", "--confirm")}}}}, nil
	}
	deleted, err := deps.Sessions.CleanupExpired(ctx, storage.Now().AddDate(0, 0, -cleanupRetentionDays(deps)))
	if err != nil {
		if deleted > 0 {
			return sessionChange(ctx, deps, nil, "", deleted, "clean"), err
		}
		return nil, err
	}
	return sessionChange(ctx, deps, nil, "", deleted, "clean"), nil
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
	handle      func(context.Context, Deps, command.Request) (*command.Result, error)
}

func (c sessionTargetCommand) Info() command.Info {
	return command.Info{Name: c.name, Args: c.args, Description: c.description, Help: c.help, SessionEffect: command.SessionEffectMutate, MinRole: contextinfo.RoleUser}
}

func (c sessionTargetCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
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

func parseRenameArgs(ctx context.Context, deps Deps, args string) (string, string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", "", inputError(fmt.Errorf("usage: %s", deps.CommandText("rename", "[session_id|current_title]", "<title>")))
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
		return "", true, inputError(fmt.Errorf("session title %q matches multiple sessions; %s → 用 ID", target, deps.CommandText("sessions")))
	}
}

func parseSessionsArgs(args string) (int, string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return 0, "", nil
	}
	page := 0
	if fields[0] != "-k" && fields[0] != "--keyword" {
		var err error
		page, err = strconv.Atoi(fields[0])
		if err != nil || page < 1 {
			return 0, "", inputError(fmt.Errorf("expected page≥1 or -k/--keyword"))
		}
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return page, "", nil
	}
	if (fields[0] != "-k" && fields[0] != "--keyword") || len(fields) < 2 {
		return 0, "", inputError(fmt.Errorf("expected -k/--keyword <keyword>"))
	}
	return page, strings.Join(fields[1:], " "), nil
}

func parseResumePageArg(args string) (int, error) {
	fields := strings.Fields(args)
	if len(fields) != 2 || fields[0] != "--page" {
		return 0, inputError(fmt.Errorf("expected --page <page>"))
	}
	page, err := strconv.Atoi(fields[1])
	if err != nil || page < 1 {
		return 0, inputError(fmt.Errorf("page must be a positive number"))
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

func confirmCommandMessage(ctx context.Context, deps Deps, name, target string) (*command.Result, error) {
	id, err := resolveSessionTarget(ctx, deps, target)
	if err != nil {
		return nil, err
	}
	row, err := deps.Store.Sessions().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !session.CanAccess(deps.Scope(ctx), row) {
		return nil, &command.Error{Kind: command.ErrorRejected, Code: command.CodePermissionDenied, Message: "无权访问该会话"}
	}
	action := deps.CommandText(name, id, "--confirm")
	text := fmt.Sprintf("%s? %s · %s\n%s", name, emptyTitle(row.Title), id, action)
	return &command.Result{Kind: command.ResultKindSession, Data: command.SessionChangeData{Session: sessionData(row), SessionID: id, CurrentSessionID: currentSessionID(ctx, deps), Operation: "confirm", Prompt: text}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认", Command: action}}}}, nil
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
	return target, nil
}
