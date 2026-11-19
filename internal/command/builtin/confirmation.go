package builtin

import (
	"context"
	command "elbot/internal/command/share"

	"elbot/internal/control"
	"elbot/internal/operationresult"
)

type confirmationCommand struct {
	deps     Deps
	info     command.Info
	scope    string
	decision string
}

func (c confirmationCommand) Info() command.Info { return c.info }
func (c confirmationCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	if c.info.Name == "detail" {
		current, err := c.deps.Control.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{Refresh: true})
		if err != nil {
			return command.Result{}, err
		}
		if current.Status != operationresult.Success {
			return command.TextResult(current.Status, current.Message), nil
		}
		if current.Data.Kind != "tool" {
			return command.TextResult(operationresult.Success, "无待确认"), nil
		}
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Blocks: current.Data.Detail, Actions: current.Data.Actions}, nil
	}
	result, err := c.deps.Control.RespondConfirmation(ctx, control.RespondConfirmationRequest{Decision: c.decision, Scope: c.scope, Extra: req.Args})
	if err != nil {
		return command.Result{}, err
	}
	if result.Status != operationresult.Success {
		return command.TextResult(result.Status, result.Message), nil
	}
	data := result.Data
	text := ""
	if data.Accepted {
		text = "已确认"
		if c.decision == "reject" {
			text = "已拒绝"
		}
	}
	r := command.TextResult(result.Status, text)
	r.Data = data
	r.Kind = command.ResultKindText
	r.Continuation = data.Continuation
	return r, nil
}

type ConfirmationModule struct{}

func (ConfirmationModule) RegisterCommands(registrar Registrar, deps Deps) error {
	for _, item := range command.ConfirmationCommands() {
		if err := registrar.Register(confirmationCommand{deps: deps, info: item.Info, scope: item.Scope, decision: item.Decision}); err != nil {
			return err
		}
	}
	return nil
}
