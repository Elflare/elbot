package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"elbot/internal/agent/execution"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type coagentHandler struct {
	sessions  *session.Supervisor
	execution *execution.ExecutionCoordinator
	identity  *agentshare.IdentityResolver
	models    ModelCapabilities
	platform  PlatformCapabilities
	lifetime  context.Context
}

// Board inputs keep the original foreground address even when a member posts another message.
type coagentHomeKey struct{}

func (h coagentHandler) handle(ctx context.Context, action session.CoagentAction, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	scope := h.identity.Scope(ctx)
	if user && action.Operation == "create" {
		row, err := h.sessions.GetOrCreateCurrent(ctx, scope, action.Goal)
		if err != nil || row.Status != operationresult.Success {
			return operationresult.Result[storage.CoagentBoard]{Status: row.Status, Message: row.Message}, err
		}
	}
	current, err := h.sessions.CurrentBound(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentBoard]{Status: current.Status, Message: current.Message}, err
	}
	b := current.Data.Binding
	if original, ok := session.BindingFromContext(ctx); ok && original != b {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Conflict, Message: "当前会话已切换"}, nil
	}
	if action.After < 0 {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.InvalidArguments, Message: "after 须为非负整数"}, nil
	}
	if action.Operation == "board" {
		return h.sessions.ReadCoagentBoard(ctx, b, action.After)
	}
	var result operationresult.Result[storage.CoagentEntry]
	switch action.Operation {
	case "create":
		if current.Data.Session.TaskType == "coagent" || current.Data.Session.Mode != storage.SessionModeWork {
			return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Forbidden, Message: "请在主工作会话中创建成员"}, nil
		}
		if user {
			selected := h.models.ResolveMode(current.Data.Session.Mode)
			if selected.Client == nil {
				return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Unavailable, Message: "主模型不可用"}, nil
			}
			existing, err := h.sessions.ReadCoagentBoard(ctx, b, math.MaxInt64)
			if err != nil {
				return operationresult.Result[storage.CoagentBoard]{}, err
			}
			used := max(0, len(existing.Data.Members)-1)
			if action.Count > storage.MaxCoagents-used {
				return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Conflict, Message: fmt.Sprintf("%s，还可新增 %d 个", storage.ErrCoagentLimit, max(0, storage.MaxCoagents-used))}, nil
			}
			var names []string
			for _, m := range existing.Data.Members {
				names = append(names, m.Name)
			}
			answer, err := selected.Client.GenerateText(ctx, llm.TextRequest{Model: selected.Model, Instructions: fmt.Sprintf("为此次协作新增的 %d 位成员起互不重复的单行中文名。已占用的名字为 %s。只输出 JSON 字符串数组。", action.Count, strings.Join(names, "、")), Input: action.Goal})
			if err != nil {
				return operationresult.Result[storage.CoagentBoard]{}, err
			}
			if err := json.Unmarshal([]byte(answer.Text), &action.Names); err != nil || len(action.Names) != action.Count {
				return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Rejected, Message: "模型未返回指定数量的成员名"}, nil
			}
		}
		result, err = h.sessions.CreateCoagents(ctx, b, action.Goal, action.Names, user)
	case "pin":
		result, err = h.sessions.PinCoagentBoard(ctx, b, user, action.Pin)
	case "post":
		result, err = h.sessions.PostCoagentEntry(ctx, b, user, storage.CoagentEntry{Text: action.Text, TargetSessionID: action.TargetSessionID})
	default:
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.InvalidArguments, Message: "未知协作操作"}, nil
	}
	if err != nil || result.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentBoard]{Status: result.Status, Message: result.Message}, err
	}
	board, err := h.sessions.ReadCoagentBoard(ctx, b, result.Data.Number-1)
	if err != nil || board.Status != operationresult.Success {
		return board, err
	}
	board.Data.Entries = []storage.CoagentEntry{result.Data}
	text := session.FormatCoagentEntry(result.Data, board.Data.Members)
	if result.Data.Pinned {
		text = session.FormatCoagentBoard(board.Data)
	}
	base, ok := ctx.Value(coagentHomeKey{}).(context.Context)
	if !ok {
		home, _ := contextinfo.ConversationFromContext(ctx)
		home.InputID, home.PlatformMessageID, home.ReplyToMessageID = "", "", ""
		base = h.platform.RestoreMessageContext(h.lifetime, ctx)
		base = contextinfo.WithActor(base, h.identity.Actor(ctx))
		base = contextinfo.WithConversation(base, home)
	}
	home, _ := contextinfo.ConversationFromContext(base)
	display := contextinfo.WithExecution(base, contextinfo.Execution{SessionID: board.Data.MainSessionID, Mode: storage.SessionModeWork})
	// A user command presents its own structured reply; tool writes need a separate display.
	var displayErr error
	if !user {
		sent, err := h.platform.SendTo(display, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: text}})
		displayErr = err
		if err == nil && sent.Status != operationresult.Success {
			displayErr = fmt.Errorf("协作留言已保存，主窗口展示失败：%s", sent.Message)
		}
	}
	for _, member := range board.Data.Members {
		if target := result.Data.TargetSessionID; target != "" && target != member.SessionID {
			continue
		}
		info := home
		main := member.SessionID == board.Data.MainSessionID
		memberBase := base
		if !main {
			info.TargetSessionID = member.SessionID
			// Keep the foreground reply route, but give hooks and tools only this board input.
			memberBase = contextinfo.WithActor(h.lifetime, h.identity.Actor(ctx))
		}
		memberCtx := contextinfo.WithConversation(context.WithValue(memberBase, coagentHomeKey{}, base), info)
		memberCtx = agentshare.WithMessageContext(memberCtx, agentshare.MessageContext{RestoreMessageContext: h.platform.RestoreMessageContext})
		err := h.execution.QueueBoardInput(memberCtx, board.Data.ID, member, text, main, func(sendCtx context.Context, body string) (delivery.Receipt, error) {
			if strings.TrimSpace(body) == "" {
				return delivery.Receipt{}, nil
			}
			live, cancel := context.WithCancel(display)
			stop := context.AfterFunc(sendCtx, cancel)
			defer func() { stop(); cancel() }()
			r, err := h.platform.SendTo(live, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: "[" + member.Name + "]：" + body}})
			if err == nil && r.Status != operationresult.Success {
				err = fmt.Errorf("%s", r.Message)
			}
			return r.Data, err
		})
		if err != nil && displayErr == nil {
			displayErr = err
		}
	}
	return board, displayErr
}
