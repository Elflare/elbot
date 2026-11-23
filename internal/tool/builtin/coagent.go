package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

type coagentOperations interface {
	Coagent(context.Context, session.CoagentAction, bool) (operationresult.Result[storage.CoagentBoard], error)
}

func coagentTools(ops coagentOperations) []share.ToolDefinition {
	entry := share.ToolDefinition{Definition: share.NewBuilder("coagent").ForegroundOnly().OwnerScoped().Description("协作成员与留言板").BuildInfo(),
		LoadDetail: func(context.Context) (share.DetailBlock, error) {
			return share.DetailBlock{Content: "使用已开放的 coagent 工具协作。发帖可广播或定向给组内 Session ID，读板不会唤醒成员。"}, nil
		},
	}
	var tools []share.ToolDefinition
	for _, op := range []string{"create", "post", "pin", "board", "remove"} {
		b := share.NewBuilder("coagent_" + op).Hidden().ForegroundOnly().OwnerScoped().Tags("coagent").Risk(share.RiskLow)
		switch op {
		case "create":
			b.Risk(share.RiskHigh).Description("创建同组协作成员并发布置顶。").String("goal", "协作目标和分工", share.Required()).StringArray("names", "新增成员的显示名", share.Required())
		case "post":
			b.Description("向本组留言，默认广播，指定目标时仅向该成员推送。").String("text", "留言正文", share.Required()).String("target_session_id", "定向收件 Session ID")
		case "pin":
			b.Description("修改目标、自由正文或两者，统一广播一次。").String("goal", "完整的新目标和分工").String("text", "自由正文；显式空字符串清空，省略保留")
		case "remove":
			b.Risk(share.RiskHigh).Description("停止并移除指定子成员，保留会话和历史留言。").String("target_session_id", "要移除的本组成员 Session ID", share.Required())
		case "board":
			b.Description("查看置顶与名册，按编号升序读取全部或新增留言。").Integer("after", "只返回该编号之后的留言")
		}
		properties := b.BuildSchema().Parameters["properties"].(map[string]any)
		def := b.Build(func(_ context.Context, req share.CallRequest, info share.Info) (operationresult.Result[*share.PreparedCall], error) {
			action := session.CoagentAction{Operation: op}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(req.Arguments, &fields); err != nil || fields == nil {
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: "工具参数须为 JSON 对象"}, nil
			}
			for key, raw := range fields {
				if _, valid := properties[key]; !valid || string(raw) == "null" {
					return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: "无效的工具参数：" + key}, nil
				}
			}
			var args any = &action
			if op == "pin" {
				args = &action.Pin
			}
			if err := json.Unmarshal(req.Arguments, args); err != nil {
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
			}
			if op == "create" {
				names := session.ValidateCoagentNames(action.Goal, action.Names)
				if names.Status != operationresult.Success {
					return operationresult.Result[*share.PreparedCall]{Status: names.Status, Message: names.Message}, nil
				}
				action.Names = names.Data
			}
			if _, directed := fields["target_session_id"]; (directed || op == "remove") && strings.TrimSpace(action.TargetSessionID) == "" {
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: "目标 Session ID 不能为空"}, nil
			}
			var details []share.ConfirmationDetailBlock
			if op == "create" {
				details = []share.ConfirmationDetailBlock{share.FieldsConfirmationDetail(
					share.ConfirmationField{Key: "goal", Label: "任务目标", Value: action.Goal},
					share.ConfirmationField{Key: "names", Label: "新增成员", Value: strings.Join(action.Names, "、")})}
			} else if op == "remove" {
				details = []share.ConfirmationDetailBlock{share.FieldsConfirmationDetail(share.ConfirmationField{Key: "target_session_id", Label: "移除成员", Value: action.TargetSessionID})}
			}
			call := share.NewPreparedCall(req, share.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
				res, err := ops.Coagent(ctx, action, false)
				if err != nil || res.Status != operationresult.Success {
					return operationresult.Result[*share.Result]{Status: res.Status, Message: res.Message}, res.Message, err
				}
				content := session.FormatCoagentBoard(res.Data)
				return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{Content: content}}, content, nil
			})
			return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: call}, nil
		})
		def.Parameters["additionalProperties"] = false
		if op == "pin" {
			def.Parameters["anyOf"] = []any{map[string]any{"required": []string{"goal"}}, map[string]any{"required": []string{"text"}}}
		}
		for key, raw := range properties {
			p := raw.(map[string]any)
			switch key {
			case "names":
				p["minItems"], p["maxItems"], p["uniqueItems"] = 1, storage.MaxCoagents, true
				p["items"].(map[string]any)["minLength"] = 1
			case "after":
				p["minimum"], p["default"] = 0, 0
			default:
				if op != "pin" || key != "text" {
					p["minLength"] = 1
				}
			}
		}
		tools = append(tools, def)
		entry.ActivateTools = append(entry.ActivateTools, def.Definition.Name)
	}
	return append(tools, entry)
}
