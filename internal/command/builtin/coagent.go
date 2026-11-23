package builtin

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
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
	return command.Info{Name: "co", Args: "create <人数> <目标> | post [--to <session_id>] <正文> | pin [正文] | pin --goal <目标> | board | status [成员ID] | remove <成员ID>", Description: "管理协作成员、查看状态或访问留言板", MinRole: contextinfo.RoleUser}
}

func (c coagentCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	args := strings.Fields(req.Raw[len(req.Prefix)+len(req.Name) : token.Start])
	if len(args) == 0 {
		return completeStringOptions([]string{"create", "post", "pin", "board", "status", "remove"}, token.Text, token.Start, token.End, "co_action")
	}
	switch strings.Join(args, " ") {
	case "post":
		return completeStringOptions([]string{"--to"}, token.Text, token.Start, token.End, "option")
	case "pin":
		return completeStringOptions([]string{"--goal"}, token.Text, token.Start, token.End, "option")
	case "create", "post --to", "status", "remove":
		board, err := c.deps.Control.Coagent(ctx, session.CoagentAction{Operation: "board", After: math.MaxInt64}, true)
		if err != nil {
			return nil
		}
		if args[0] == "create" {
			if board.Status != operationresult.Success && board.Status != operationresult.NotFound {
				return nil
			}
			var counts []string
			for i := 1; i <= storage.MaxCoagents-max(0, len(board.Data.Members)-1); i++ {
				counts = append(counts, strconv.Itoa(i))
			}
			return completeStringOptions(counts, token.Text, token.Start, token.End, "count")
		}
		if board.Status != operationresult.Success {
			return nil
		}
		var options []command.CompletionOption
		for _, m := range board.Data.Members {
			if args[0] == "remove" && m.SessionID == board.Data.MainSessionID {
				continue
			}
			options = append(options, command.CompletionOption{Text: m.SessionID, Description: m.Name})
		}
		return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "session_id")
	}
	return nil
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
	case "status", "remove":
		target, extra := questionArg(body)
		if extra != "" || (op == "remove" && target == "") {
			return invalid()
		}
		if op == "status" {
			result, err := c.deps.Control.CoagentStatus(ctx, target)
			return command.FromOperationResult(result, command.ResultKindCoagentStatus, func(items []control.CoagentStatus) string {
				lines := make([]string, len(items))
				for i, m := range items {
					lines[i] = fmt.Sprintf("%s [%s]：%s · pending=%d", m.Name, m.SessionID, m.Phase, m.PendingCount)
				}
				return strings.Join(lines, "\n")
			}), err
		}
		action.TargetSessionID = target
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
