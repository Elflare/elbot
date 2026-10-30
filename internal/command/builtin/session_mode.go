package builtin

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
)

func NewWork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "work",
		Usage:         "/work [message]",
		Description:   "切换 work",
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeWork, NewSessionTitle: "New work session"})
		if err != nil {
			return nil, err
		}
		content := fmt.Sprintf("%s · %s", activated.Session.Mode, activated.Session.ID)
		if activated.AlreadyActive {
			content = fmt.Sprintf("work · %s · unchanged", activated.Session.ID)
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
		Description:   "切换 chat",
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "New chat session"})
		if err != nil {
			if errors.Is(err, sessionpkg.ErrChatModeRequiresEmptySession) {
				return &command.Result{Content: "已有对话记录，请先 /new 再 /chat"}, nil
			}
			return nil, err
		}
		content := fmt.Sprintf("%s · %s", activated.Session.Mode, activated.Session.ID)
		if activated.AlreadyActive {
			content = fmt.Sprintf("chat · %s · unchanged", activated.Session.ID)
		}
		result := &command.Result{Content: content}
		if req.Args != "" {
			result.Continuation = &command.Continuation{Text: req.Args, SessionID: activated.Session.ID}
		}
		return result, nil
	})
}
