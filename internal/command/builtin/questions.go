package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"fmt"
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
		{Name: "ac", Description: "提交全部待答题，空题记为未回答", MinRole: contextinfo.RoleUser},
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
			return command.TextResult(operationresult.InvalidArguments, "用法："+c.deps.CommandText("ac")), nil
		}
		answer, err := c.deps.Control.ConfirmAnswers(ctx, "")
		return command.FromOperationResult(answer, command.ResultKindAnswers, func(d control.ConfirmAnswersResult) string { return fmt.Sprintf("已提交 %d 题", len(d.QuestionIDs)) }), err
	}
	if req.Args == "" {
		items, err := c.deps.Control.ListQuestions(ctx, "")
		if items.Status != operationresult.Success {
			return command.TextResult(items.Status, items.Message), err
		}
		if len(items.Data) == 0 {
			return command.TextResult(items.Status, "无待答题"), err
		}
		out := delivery.QuestionsOutput(items.Data, c.deps.CommandText("a"), c.deps.CommandText("ac"))
		return command.Result{Status: items.Status, Kind: command.ResultKindQuestions, Blocks: out.Blocks, Actions: out.Actions}, err
	}
	// Split only the two command arguments, preserving the user's remaining text.
	number, rest := questionArg(req.Args)
	choice, extra := questionArg(rest)
	id, err := strconv.ParseInt(number, 10, 64)
	if err != nil || id <= 0 || choice == "" {
		return command.TextResult(operationresult.InvalidArguments, "用法："+c.deps.CommandText("a", "<题号> <选项|o> [文本]")), nil
	}
	draft := control.QuestionDraft{Text: extra}
	if choice != "o" {
		n, err := strconv.Atoi(choice)
		if err != nil {
			return command.TextResult(operationresult.InvalidArguments, "需选项号或 o"), nil
		}
		draft.Choice = &n
	}
	saved, err := c.deps.Control.SaveQuestionDraft(ctx, "", id, draft)
	return command.FromOperationResult(saved, command.ResultKindQuestionDraft, func(d control.Question) string { return fmt.Sprintf("#%d 已暂存", d.ID) }), err
}
func questionArg(s string) (string, string) {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}
