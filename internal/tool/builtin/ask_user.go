package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

type askUserTool struct {
	create   func([]storage.Question)
	commands func() (string, string)
}

func (t askUserTool) definition() share.ToolDefinition {
	fields := map[string]any{
		"question": map[string]any{"type": "string"},
		"options":  map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": map[string]any{"type": "string"}},
	}
	return share.NewBuilder("ask_user").ForegroundOnly().OwnerScoped().Risk(share.RiskLow).
		Description("批量提问，推荐项在前。立即返回；勿复述或轮询。答案以 user 消息送达。").
		ObjectArray("questions", "题目", fields, []string{"question", "options"}, share.Required()).Build(t.prepare)
}

func (t askUserTool) prepare(_ context.Context, req share.CallRequest, info share.Info) (operationresult.Result[*share.PreparedCall], error) {
	var args struct {
		Questions []struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		} `json:"questions"`
	}
	invalid := func(message string) (operationresult.Result[*share.PreparedCall], error) {
		return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: message}, nil
	}
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return invalid(err.Error())
	}
	if len(args.Questions) == 0 {
		return invalid("缺少题目")
	}
	questions := make([]storage.Question, 0, len(args.Questions))
	for i, item := range args.Questions {
		q := storage.Question{Question: strings.TrimSpace(item.Question), Options: item.Options}
		if q.Question == "" || len(q.Options) < 1 || len(q.Options) > 3 {
			return invalid(fmt.Sprintf("第 %d 题：需题干和 1–3 项", i+1))
		}
		for j, option := range q.Options {
			q.Options[j] = strings.TrimSpace(option)
			if q.Options[j] == "" {
				return invalid(fmt.Sprintf("第 %d 题：空选项", i+1))
			}
		}
		questions = append(questions, q)
	}
	prepared := share.NewPreparedCall(req, share.RiskAssessment{Level: info.Risk}, nil, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		for i := range questions {
			questions[i].SessionID = facts.SessionID
		}
		t.create(questions)
		status := fmt.Sprintf("%d 题待答", len(questions))
		answer, confirm := t.commands()
		out := delivery.QuestionsOutput(questions, answer, confirm)
		return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{Content: status, Outputs: []delivery.Output{out}}}, status, nil
	})
	return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: prepared}, nil
}
