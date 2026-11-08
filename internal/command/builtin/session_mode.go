package builtin

import (
	"context"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
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
		data, err := deps.Control.SetMode(ctx, control.SetModeRequest{Mode: storage.SessionModeWork, Text: req.Args, NewSessionTitle: "New work session"})
		return sessionChange(data), err
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
		data, err := deps.Control.SetMode(ctx, control.SetModeRequest{Mode: storage.SessionModeChat, Text: req.Args, NewSessionTitle: "New chat session"})
		return sessionChange(data), err
	})
}
