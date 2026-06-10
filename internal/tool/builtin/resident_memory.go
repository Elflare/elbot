package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/tool"
)

const ResidentMemoryToolName = "resident_memory"

type ResidentMemoryTool struct {
	Store *resident.Store
}

type toolArgs struct {
	Action  string `json:"action"`
	Content string `json:"content"`
}

type toolData struct {
	Content string `json:"content,omitempty"`
	Status  string `json:"status"`
}

func NewResidentMemoryTool(store *resident.Store) ResidentMemoryTool {
	return ResidentMemoryTool{Store: store}
}

func (t ResidentMemoryTool) Name() string {
	return ResidentMemoryToolName
}

func (t ResidentMemoryTool) Info() tool.Info {
	return toolBuilder().BuildInfo()
}

func (t ResidentMemoryTool) Schema() llm.ToolSchema {
	return toolBuilder().BuildSchema()
}

func toolBuilder() *tool.Builder {
	return tool.NewBuilder(ResidentMemoryToolName).
		Description("读取、追加、覆盖或删除当前用户在当前平台的常驻记忆。不超过 400 字；新增少量信息优先用 append，整合旧内容时用 write 写入完整新记忆。用write前务必先读取记忆以防覆盖丢失。").
		Risk(tool.RiskLow).
		String("action", "操作类型：read、append、write 或 delete。", tool.Required()).
		String("content", "append/write 时使用。append 会追加到旧内容后；write 会覆盖旧内容并要求写完整的新常驻记忆内容。")
}

func (t ResidentMemoryTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.Store == nil {
		return nil, fmt.Errorf("resident memory store is not configured")
	}
	actor, ok := security.ActorFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("resident memory actor is not available")
	}
	scope := resident.ActorScope(actor)
	var args toolArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse resident_memory arguments: %w", err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "read", "get":
		return t.read(ctx, scope)
	case "append", "add":
		return t.append(ctx, scope, args.Content)
	case "write", "set", "update":
		return t.write(ctx, scope, args.Content)
	case "delete", "clear":
		return t.delete(ctx, scope)
	default:
		return nil, fmt.Errorf("action must be read, append, write or delete")
	}
}

func (t ResidentMemoryTool) read(ctx context.Context, scope session.Scope) (*tool.Result, error) {
	content, err := t.Store.Read(ctx, scope)
	if errors.Is(err, resident.ErrNotFound) {
		return encodedResult("常驻记忆为空。", toolData{Status: "empty"})
	}
	if err != nil {
		return nil, err
	}
	return encodedResult(content, toolData{Status: "ok", Content: content})
}

func (t ResidentMemoryTool) append(ctx context.Context, scope session.Scope, content string) (*tool.Result, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("resident memory content is required")
	}
	existing, err := t.Store.Read(ctx, scope)
	if errors.Is(err, resident.ErrNotFound) {
		return t.write(ctx, scope, content)
	}
	if err != nil {
		return nil, err
	}
	merged := strings.TrimSpace(existing)
	if merged == "" {
		merged = content
	} else {
		merged += "\n" + content
	}
	if err := t.Store.Write(ctx, scope, merged); err != nil {
		if strings.Contains(err.Error(), "too long") {
			return encodedResult("追加后会超过 400 字。请先 read 当前记忆，再整合为不超过 400 字的完整内容后用 write 覆盖。", toolData{Status: "too_long", Content: existing})
		}
		return nil, err
	}
	return encodedResult("已追加常驻记忆。", toolData{Status: "appended", Content: merged})
}

func (t ResidentMemoryTool) write(ctx context.Context, scope session.Scope, content string) (*tool.Result, error) {
	if err := t.Store.Write(ctx, scope, content); err != nil {
		return nil, err
	}
	content = strings.TrimSpace(content)
	return encodedResult("已更新常驻记忆。", toolData{Status: "updated", Content: content})
}

func (t ResidentMemoryTool) delete(ctx context.Context, scope session.Scope) (*tool.Result, error) {
	err := t.Store.Delete(ctx, scope)
	if errors.Is(err, resident.ErrNotFound) {
		return encodedResult("常驻记忆本来就是空的。", toolData{Status: "empty"})
	}
	if err != nil {
		return nil, err
	}
	return encodedResult("已删除常驻记忆。", toolData{Status: "deleted"})
}

func encodedResult(content string, data toolData) (*tool.Result, error) {
	_ = data
	return &tool.Result{Content: content}, nil
}
