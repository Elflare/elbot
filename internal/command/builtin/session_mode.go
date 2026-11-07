package builtin

import (
	"context"
	"errors"
	"fmt"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
)

func NewWork(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "work",
		Args:          "[message]",
		Description:   "切换 work，启用工具",
		Help:          "无会话→新建；message 在切换成功后发送",
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeWork, NewSessionTitle: "New work session"})
		if err != nil {
			return nil, err
		}
		result := sessionChange(ctx, deps, activated.Session, activated.Session.ID, 1, "work")
		data := result.Data.(command.SessionChangeData)
		data.Unchanged = activated.AlreadyActive
		result.Data = data
		if req.Args != "" {
			result.Continuation = &command.Continuation{Kind: command.ContinuationInput, Text: req.Args, SessionID: activated.Session.ID}
		}
		return result, nil
	})
}

func NewChat(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "chat",
		Args:          "[message]",
		Description:   "切换 chat，不注入工具",
		Help:          "无会话→新建；message 在切换成功后发送\nwork 已有历史→先 " + deps.CommandText("new"),
		SessionEffect: command.SessionEffectSwitchCurrent,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		activated, err := deps.Sessions.ActivateMode(ctx, deps.Scope(ctx), sessionpkg.ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "New chat session"})
		if err != nil {
			if errors.Is(err, sessionpkg.ErrChatModeRequiresEmptySession) {
				return nil, &command.Error{Kind: command.ErrorRejected, Code: "mode_conflict", Message: fmt.Sprintf("已有对话记录，请先 %s 再 %s", deps.CommandText("new"), deps.CommandText("chat")), Cause: err}
			}
			return nil, err
		}
		result := sessionChange(ctx, deps, activated.Session, activated.Session.ID, 1, "chat")
		data := result.Data.(command.SessionChangeData)
		data.Unchanged = activated.AlreadyActive
		result.Data = data
		if req.Args != "" {
			result.Continuation = &command.Continuation{Kind: command.ContinuationInput, Text: req.Args, SessionID: activated.Session.ID}
		}
		return result, nil
	})
}
