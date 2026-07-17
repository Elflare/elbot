package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/security"
	"elbot/internal/storage"
)

const resumeHistoryTurns = 3 // TODO: 后续支持配置恢复时展示多少轮历史。
const defaultSessionListPageSize = 10
const messageListPageSize = 20

func NewArchive(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "archive",
		usage:       "/archive [number|session_id] --confirm",
		description: "Archive a session. Defaults to current session.",
		confirm:     true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
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
			return &command.Result{Content: fmt.Sprintf("archived session:\n  id: %s\n  title: %s", session.ID, session.Title)}, nil
		},
	}
}

func NewUnarchive(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "unarchive",
		usage:       "/unarchive [number|session_id]",
		description: "Unarchive a session. Defaults to current session.",
		archived:    true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Unarchive(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return &command.Result{Content: fmt.Sprintf("unarchived session:\n  id: %s\n  title: %s", session.ID, session.Title)}, nil
		},
	}
}

func NewPin(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "pin",
		usage:       "/pin [number|session_id]",
		description: "Pin a session. Defaults to current session.",
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Pin(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return &command.Result{Content: fmt.Sprintf("pinned session:\n  id: %s\n  title: %s", session.ID, session.Title)}, nil
		},
	}
}

func NewUnpin(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "unpin",
		usage:       "/unpin [number|session_id]",
		description: "Unpin a session. Defaults to current session.",
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
			sessionID, err := resolveSessionTarget(ctx, deps, strings.TrimSpace(req.Args))
			if err != nil {
				return nil, err
			}
			session, err := deps.Sessions.Unpin(ctx, deps.Scope(ctx), sessionID)
			if err != nil {
				return nil, err
			}
			return &command.Result{Content: fmt.Sprintf("unpinned session:\n  id: %s\n  title: %s", session.ID, session.Title)}, nil
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
		Usage:         "/rename [number|session_id|current_title] <title>",
		Description:   "Rename current or selected session.",
		SessionEffect: command.SessionEffectMutate,
		MinRole:       security.RoleUser,
		Help:          "Usage:\n  /rename <title>\n  /rename <number|session_id|current_title> <title>\n\nIf current_title matches more than one visible session, use /sessions and rename by number or session id.",
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
	return &command.Result{Content: fmt.Sprintf("renamed session:\n  id: %s\n  title: %s", session.ID, session.Title)}, nil
}

func (c renameCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := currentCompletionToken(req)
	if !isFirstArg(req, token) {
		return nil
	}
	return completeSessionIDs(ctx, c.deps, token.Text, false, token.Start, token.End)
}

func NewDelete(deps Deps) command.Handler {
	return sessionTargetCommand{
		deps:        deps,
		name:        "delete",
		usage:       "/delete <number|session_id> --confirm",
		description: "Delete a session permanently.",
		confirm:     true,
		handle: func(ctx context.Context, deps Deps, req command.Request) (*command.Result, error) {
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
			return &command.Result{Content: fmt.Sprintf("deleted session:\n  id: %s", sessionID)}, nil
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
		Usage:         "/clean --confirm",
		Description:   "Delete expired non-archived and non-pinned sessions.",
		SessionEffect: command.SessionEffectMutate,
	}
}

func (c cleanCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	_, confirmed := parseTargetConfirm(req.Args)
	if !confirmed {
		return &command.Result{Content: "clean will permanently delete expired sessions that are not archived or pinned. Run /clean --confirm to continue."}, nil
	}
	deleted, err := deps.Sessions.CleanupExpired(ctx, storage.Now().AddDate(0, 0, -cleanupRetentionDays(deps)))
	if err != nil {
		return nil, err
	}
	return &command.Result{Content: fmt.Sprintf("cleaned expired sessions: %d", deleted)}, nil
}

func (c cleanCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := currentCompletionToken(req)
	if !isFirstArg(req, token) {
		return nil
	}
	return completeConfirmFlag(req.Args, token)
}

type sessionTargetCommand struct {
	deps        Deps
	name        string
	usage       string
	description string
	archived    bool
	confirm     bool
	handle      func(context.Context, Deps, command.Request) (*command.Result, error)
}

func (c sessionTargetCommand) Info() command.Info {
	return command.Info{Name: c.name, Usage: c.usage, Description: c.description, SessionEffect: command.SessionEffectMutate, MinRole: security.RoleUser}
}

func (c sessionTargetCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	return c.handle(ctx, c.deps, req)
}

func (c sessionTargetCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := currentCompletionToken(req)
	fields := strings.Fields(req.Args)
	if len(fields) > 0 && fields[len(fields)-1] == "--confirm" {
		return nil
	}
	if c.confirm && len(fields) > 0 && (!isFirstArg(req, token) || strings.HasPrefix(token.Text, "--")) {
		return completeConfirmFlag(req.Args, token)
	}
	if isFirstArg(req, token) {
		return completeSessionIDs(ctx, c.deps, token.Text, c.archived, token.Start, token.End)
	}
	if c.confirm {
		return completeConfirmFlag(req.Args, token)
	}
	return nil
}

func auditCommand(deps Deps, event string, attrs ...any) {
	if deps.Audit == nil {
		return
	}
	deps.Audit(event, attrs...)
}

func cleanupRetentionDays(deps Deps) int {
	_, days := deps.SessionState.config()
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
		ids := deps.SessionState.get(deps.Scope(ctx))
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
	return fmt.Sprintf("%s will modify session:\n  title: %s\n  id: %s\nRun /%s %s --confirm to continue.", name, emptyTitle(session.Title), session.ID, name, session.ID), nil
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
		ids := deps.SessionState.get(deps.Scope(ctx))
		if idx < 1 || idx > len(ids) {
			return "", fmt.Errorf("session index %d out of range; run /sessions or /archives first", idx)
		}
		return ids[idx-1], nil
	}
	return target, nil
}
