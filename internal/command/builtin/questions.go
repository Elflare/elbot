package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"strconv"
	"strings"
	"unicode"
)

type questionsModule struct{}
type questionCommand struct {
	deps Deps
	info command.Info
}

func (questionsModule) RegisterCommands(r Registrar, deps Deps) error {
	for _, info := range []command.Info{
		{Name: "a", Args: "[题号 选项|o [文本]]", Description: "看题 / 填草稿", MinRole: contextinfo.RoleUser},
		{Name: "ac", Description: "提交草稿", MinRole: contextinfo.RoleUser},
	} {
		if err := r.Register(questionCommand{deps, info}); err != nil {
			return err
		}
	}
	return nil
}
func (c questionCommand) Info() command.Info { return c.info }
func (c questionCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	if c.info.Name == "ac" {
		if req.Args != "" {
			return command.Result{Status: operationresult.InvalidArguments, Message: "用法：" + c.deps.CommandText("ac")}, nil
		}
		answer, err := c.deps.Control.ConfirmAnswers(ctx, "")
		result := command.FromOperationResult(answer, command.ResultKindAnswers)
		result.Continuation = answer.Data.Continuation
		return result, err
	}
	if req.Args == "" {
		items, err := c.deps.Control.ListQuestions(ctx, "")
		return command.FromOperationResult(items, command.ResultKindQuestions), err
	}
	// Split only the two command arguments, preserving the user's remaining text.
	number, rest := questionArg(req.Args)
	choice, extra := questionArg(rest)
	id, err := strconv.ParseInt(number, 10, 64)
	if err != nil || id <= 0 || choice == "" {
		return command.Result{Status: operationresult.InvalidArguments, Message: "用法：" + c.deps.CommandText("a", "<题号> <选项|o> [文本]")}, nil
	}
	draft := control.QuestionDraft{Text: extra}
	if choice != "o" {
		n, err := strconv.Atoi(choice)
		if err != nil {
			return command.Result{Status: operationresult.InvalidArguments, Message: "需选项号或 o"}, nil
		}
		draft.Choice = &n
	}
	saved, err := c.deps.Control.SaveQuestionDraft(ctx, "", id, draft)
	return command.FromOperationResult(saved, command.ResultKindQuestionDraft), err
}
func questionArg(s string) (string, string) {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}
