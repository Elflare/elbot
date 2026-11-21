package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

type askUserTool struct {
	create   func([]storage.Question) *storage.QuestionGroup
	commands func() (string, string)
}

func (t askUserTool) definition() share.ToolDefinition {
	fields := map[string]any{
		"question": map[string]any{"type": "string"},
		"options":  map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": map[string]any{"type": "string"}},
	}
	return share.NewBuilder("ask_user").Async(share.AsyncByDefault).ForegroundOnly().OwnerScoped().Risk(share.RiskLow).
		Description("批量提问，默认异步。用户交卷后以工具结果回报，空题标为未回答。async=false 等待交卷。勿复述或轮询。").
		ObjectArray("questions", "题目", fields, []string{"question", "options"}, share.Required()).Tags("agent").Build(t.prepare)
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
	var group *storage.QuestionGroup
	prepared := share.NewPreparedCall(req, share.RiskAssessment{Level: info.Risk}, nil, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
		if group == nil {
			return operationresult.Result[*share.Result]{Status: operationresult.Rejected, Message: "交互工具需要前台执行器"}, "", nil
		}
		items, err := group.Wait(ctx)
		if err != nil {
			return operationresult.Result[*share.Result]{Status: operationresult.Canceled, Message: err.Error()}, "", nil
		}
		text := questionAnswers(items)
		return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{Content: text}}, text, nil
	})
	prepared.Prompt = func(sid string) (delivery.Output, func(), func()) {
		for i := range questions {
			questions[i].SessionID = sid
		}
		group = t.create(questions)
		answer, confirm := t.commands()
		return delivery.QuestionsOutput(questions, answer, confirm), group.Publish, group.Cancel
	}
	return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: prepared}, nil
}

func questionAnswers(items []storage.Question) string {
	var text strings.Builder
	for i, q := range items {
		if i > 0 {
			text.WriteByte('\n')
		}
		fmt.Fprintf(&text, "#%d %s\n", q.ID, q.Question)
		d := q.Draft
		switch {
		case d == nil:
			text.WriteString("未回答")
		case d.Choice == nil:
			fmt.Fprintf(&text, "答：%s", d.Text)
		default:
			fmt.Fprintf(&text, "选：%d. %s", *d.Choice, q.Options[*d.Choice-1])
			if d.Text != "" {
				fmt.Fprintf(&text, "\n补：%s", d.Text)
			}
		}
	}
	return text.String()
}
