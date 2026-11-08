package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
)

type rollbackCommand struct{ deps Deps }

func NewRollback(deps Deps) command.Handler { return rollbackCommand{deps: deps} }

func (rollbackCommand) Info() command.Info {
	return command.Info{
		Name: "rollback", Args: "[编号]",
		Description: "撤销文件编辑",
		Help:        "省略=备份列表；指定编号→直接撤销，无需确认\n原文件→恢复；新建文件→删除",
		MinRole:     contextinfo.RoleSuperadmin,
	}
}

func (c rollbackCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	arg := strings.TrimSpace(req.Args)
	if arg == "" {
		data, err := c.deps.Control.ListRollbacks(ctx)
		return &command.Result{Kind: command.ResultKindRollbacks, Data: data}, err
	}
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || id == 0 {
		return nil, inputError(fmt.Errorf("usage: %s", c.deps.CommandText("rollback", "[编号]")))
	}
	data, err := c.deps.Control.RollbackByID(ctx, control.RollbackByIDRequest{ID: id})
	return &command.Result{Kind: command.ResultKindRollback, Data: data}, err
}

func (c rollbackCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	records, err := c.deps.Control.ListRollbacks(ctx)
	if err != nil {
		return nil
	}
	options := make([]command.CompletionOption, 0, len(records))
	for _, record := range records {
		options = append(options, command.CompletionOption{Text: strconv.FormatUint(record.ID, 10), Description: record.Path})
	}
	return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "rollback_record")
}
