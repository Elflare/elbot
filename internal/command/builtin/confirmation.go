package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
)

type confirmationCommand struct {
	deps     Deps
	info     command.Info
	scope    string
	decision string
}

func (c confirmationCommand) Info() command.Info { return c.info }
func (c confirmationCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if c.info.Name == "detail" {
		current, err := c.deps.Control.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{Refresh: true})
		if err != nil {
			return nil, err
		}
		if current.Kind != "tool" {
			return &command.Result{Kind: command.ResultKindText, Data: command.TextData{Content: "无待确认"}}, nil
		}
		return &command.Result{Kind: command.ResultKindText, Data: current}, nil
	}
	data, err := c.deps.Control.RespondConfirmation(ctx, control.RespondConfirmationRequest{Decision: c.decision, Scope: c.scope, Extra: req.Args})
	if err != nil {
		return nil, err
	}
	if !data.Accepted {
		return &command.Result{Kind: command.ResultKindText, Data: command.TextData{Content: "无待确认"}}, nil
	}
	return &command.Result{Kind: command.ResultKindText, Data: data, Continuation: data.Continuation}, err
}

type ConfirmationModule struct{}

func (ConfirmationModule) RegisterCommands(registrar Registrar, deps Deps) error {
	for _, item := range []struct {
		name                         string
		aliases                      []string
		description, scope, decision string
	}{
		{"detail", []string{"details"}, "确认详情", "", ""},
		{"confirm", []string{"c"}, "确认本次", "current", "approve"},
		{"confirmtool", []string{"ct"}, "确认工具", "tool", "approve"},
		{"confirmall", []string{"ca"}, "确认全部", "all", "approve"},
		{"reject", nil, "拒绝", "current", "reject"},
	} {
		args := "[附加输入]"
		if item.name == "detail" {
			args = ""
		}
		if item.name == "reject" {
			args = "[原因]"
		}
		if err := registrar.Register(confirmationCommand{deps: deps, info: command.Info{Name: item.name, Aliases: item.aliases, Args: args, Description: item.description, MinRole: contextinfo.RoleUser}, scope: item.scope, decision: item.decision}); err != nil {
			return err
		}
	}
	return nil
}
