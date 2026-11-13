package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"fmt"
	"strings"
)

type hooksCommand struct{ deps Deps }

func NewHooks(deps Deps) command.Handler { return hooksCommand{deps: deps} }
func (c hooksCommand) Info() command.Info {
	return command.Info{Name: "hooks", Args: "[reload|<name>|start <id>|stop <id>|restart <id>]", Description: "Hook 管理", Help: "省略=列表；name=插件名/Hook 名，查看详情\nreload=重载配置；start/restart=启动/重启 persistent Worker\nstop=取消在途调用并停止 Worker（如有）\nid=插件 ID；stop 也支持 Hook 名"}
}
func (c hooksCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		data, err := c.deps.Control.ListHooks(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(data, command.ResultKindHooks), nil
	}
	switch fields[0] {
	case "reload":
		if len(fields) != 1 {
			return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", c.deps.CommandText("hooks", "reload"))}, nil
		}
		data, err := c.deps.Control.ReloadHooks(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(data, command.ResultKindHooks), nil
	case "start", "stop", "restart":
		if len(fields) != 2 {
			return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", c.deps.CommandText("hooks", fields[0], "<id>"))}, nil
		}
		var result operationresult.Result[struct{}]
		var err error
		switch fields[0] {
		case "start":
			result, err = c.deps.Control.StartHook(ctx, control.StartHookRequest{HookID: fields[1]})
		case "stop":
			result, err = c.deps.Control.StopHook(ctx, control.StopHookRequest{HookID: fields[1]})
		case "restart":
			result, err = c.deps.Control.RestartHook(ctx, control.RestartHookRequest{HookID: fields[1]})
		}
		if err != nil {
			return command.Result{}, err
		}
		if result.Status != operationresult.Success {
			return command.Result{Status: result.Status, Message: result.Message}, nil
		}
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: fmt.Sprintf("hook %s: %s · OK", fields[0], fields[1])}}, nil
	default:
		if len(fields) != 1 {
			return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", c.deps.CommandText("hooks", c.Info().Args))}, nil
		}
		data, err := c.deps.Control.GetHook(ctx, control.GetHookRequest{HookID: fields[0]})
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(data, command.ResultKindHooks), nil
	}
}
func (c hooksCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	data, err := c.deps.Control.ListHooks(ctx)
	if err != nil || data.Status != operationresult.Success {
		return nil
	}
	options := []command.CompletionOption{}
	if command.IsFirstArg(req, token) {
		options = []command.CompletionOption{{Text: "reload", Description: "重载 Hook"}, {Text: "start", Description: "启动 Hook"}, {Text: "stop", Description: "停止 Hook"}, {Text: "restart", Description: "重启 Hook"}}
	}
	seen := map[string]bool{}
	for _, item := range data.Data.Items {
		name := item.ID
		if name == "" {
			name = item.Name
		}
		if !seen[name] {
			options = append(options, command.CompletionOption{Text: name, Description: item.Description})
			seen[name] = true
		}
	}
	return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "hooks_arg")
}

type HookModule struct{}

func (HookModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHooks)
}
