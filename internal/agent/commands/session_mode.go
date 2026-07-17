package commands

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/command"
	"elbot/internal/security"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
)

func NewWork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "work",
		Usage:         "/work [message]",
		Description:   "Switch current session to work mode.",
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       security.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeWork, NewSessionTitle: "New work session"})
		if err != nil {
			return nil, err
		}
		content := fmt.Sprintf("work mode active:\n  id: %s\n  mode: %s\n  tools: enabled", activated.Session.ID, activated.Session.Mode)
		if activated.AlreadyActive {
			content = fmt.Sprintf("already in work mode:\n  id: %s\n  tools: enabled", activated.Session.ID)
		}
		result := &command.Result{Content: content}
		if req.Args != "" {
			result.Continuation = &command.Continuation{Text: req.Args, SessionID: activated.Session.ID}
		}
		return result, nil
	})
}

func NewChat(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "chat",
		Usage:         "/chat [message]",
		Description:   "Switch an empty session to chat mode, or create a chat session.",
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       security.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "New chat session"})
		if err != nil {
			if errors.Is(err, sessionpkg.ErrChatModeRequiresEmptySession) {
				return &command.Result{Content: "current work session has history; run /new then /chat to start a clean chat session"}, nil
			}
			return nil, err
		}
		content := fmt.Sprintf("chat mode active:\n  id: %s\n  mode: %s", activated.Session.ID, activated.Session.Mode)
		if activated.AlreadyActive {
			content = fmt.Sprintf("already in chat mode:\n  id: %s", activated.Session.ID)
		}
		result := &command.Result{Content: content}
		if req.Args != "" {
			result.Continuation = &command.Continuation{Text: req.Args, SessionID: activated.Session.ID}
		}
		return result, nil
	})
}
