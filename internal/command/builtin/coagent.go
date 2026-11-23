package builtin

import (
	"context"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type coagentModule struct{}
type coagentCommand struct{ deps Deps }

func (coagentModule) RegisterCommands(r Registrar, d Deps) error {
	return r.Register(coagentCommand{d})
}
func (c coagentCommand) Info() command.Info {
	return command.Info{Name: "co", Args: "create <人数> <目标> | post [--to <session_id>] <正文> | pin [正文] | pin --goal <目标> | board", Description: "创建协作成员（每组最多 8 个）或访问留言板", MinRole: contextinfo.RoleUser}
}
func (c coagentCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	op, body := questionArg(req.Args)
	action := session.CoagentAction{Operation: op}
	invalid := func() (command.Result, error) {
		return command.TextResult(operationresult.InvalidArguments, "用法："+c.deps.CommandText("co")+" "+c.Info().Args), nil
	}
	switch op {
	case "create":
		count, goal := questionArg(body)
		n, err := strconv.Atoi(count)
		if err != nil || n < 1 || goal == "" {
			return invalid()
		}
		if n > storage.MaxCoagents {
			return command.TextResult(operationresult.InvalidArguments, storage.ErrCoagentLimit.Error()), nil
		}
		action.Count, action.Goal = n, goal
	case "post":
		action.Text = body
		if flag, rest := questionArg(body); flag == "--to" {
			action.TargetSessionID, action.Text = questionArg(rest)
			if action.TargetSessionID == "" {
				return invalid()
			}
		}
		if action.Text == "" {
			return invalid()
		}
	case "pin":
		if flag, goal := questionArg(body); flag == "--goal" {
			if goal == "" {
				return invalid()
			}
			action.Pin.Goal = &goal
		} else {
			action.Pin.Text = &body
		}
	case "board":
		if strings.TrimSpace(body) != "" {
			return invalid()
		}
	default:
		return invalid()
	}
	res, err := c.deps.Control.Coagent(ctx, action, true)
	return command.FromOperationResult(res, command.ResultKindCoagent, session.FormatCoagentBoard), err
}
