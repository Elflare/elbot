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

const (
	ResidentMemoryToolName       = "resident_memory"
	ResidentMemoryReadToolName   = "resident_memory_read"
	ResidentMemoryNormalToolName = "resident_memory_normal"
	ResidentMemoryCoreToolName   = "resident_memory_core"
	residentMemoryWritingRule    = "内容必须使用第三人称：当前用户称为“用户”，模型自身称为“assistant”；只记录背景信息，不记录对模型的指令"
)

type ResidentMemoryTool struct {
	Store *resident.Store
}

type ResidentMemoryReadTool struct {
	Store *resident.Store
}

type ResidentMemoryNormalTool struct {
	Store *resident.Store
}

type ResidentMemoryCoreTool struct {
	Store *resident.Store
}

type memoryReadArgs struct {
	Section string `json:"section"`
}

type memoryNormalArgs struct {
	Action  string  `json:"action"`
	Content *string `json:"content"`
}

type memoryCoreArgs struct {
	Content *string `json:"content"`
}

type memoryReadResult struct {
	Core   string          `json:"core,omitempty"`
	Normal string          `json:"normal,omitempty"`
	Limits resident.Limits `json:"limits"`
	Status string          `json:"status"`
}

func NewResidentMemoryTools(store *resident.Store) []tool.Tool {
	return []tool.Tool{ResidentMemoryTool{Store: store}, ResidentMemoryReadTool{Store: store}, ResidentMemoryNormalTool{Store: store}, ResidentMemoryCoreTool{Store: store}}
}

func NewResidentMemoryTool(store *resident.Store) ResidentMemoryTool {
	return ResidentMemoryTool{Store: store}
}

func (t ResidentMemoryTool) Name() string { return ResidentMemoryToolName }
func (t ResidentMemoryTool) Info() tool.Info {
	return tool.NewBuilder(t.Name()).
		Description("管理当前用户在当前平台的常驻记忆。本工具仅为入口。").
		Risk(tool.RiskLow).
		DependsOn(ResidentMemoryReadToolName, ResidentMemoryNormalToolName, ResidentMemoryCoreToolName).
		BuildInfo()
}
func (t ResidentMemoryTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(t.Name()).Description(t.Info().Description).BuildSchema()
}
func (t ResidentMemoryTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	if err := validateMemoryStore(t.Store); err != nil {
		return nil, err
	}
	return &tool.Result{Content: "resident_memory 是常驻记忆管理入口。请调用 resident_memory_read、resident_memory_normal 或 resident_memory_core。"}, nil
}

func (t ResidentMemoryReadTool) Name() string { return ResidentMemoryReadToolName }
func (t ResidentMemoryReadTool) Info() tool.Info {
	return memoryBuilder(t.Name(), "读取当前用户在当前平台的常驻记忆。section 可为 all、core 或 normal，默认 all。", tool.RiskLow).Hidden().BuildInfo()
}
func (t ResidentMemoryReadTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), "读取当前用户在当前平台的常驻记忆。section 可为 all、core 或 normal，默认 all。", tool.RiskLow).
		String("section", "读取范围：all、core 或 normal。默认 all。").
		BuildSchema()
}
func (t ResidentMemoryReadTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	scope, err := memoryScope(ctx, t.Store)
	if err != nil {
		return nil, err
	}
	var args memoryReadArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	section := strings.ToLower(strings.TrimSpace(args.Section))
	if section == "" {
		section = "all"
	}
	if section != "all" && section != "core" && section != "normal" {
		return nil, fmt.Errorf("section must be all, core or normal")
	}
	memory, err := t.Store.Read(ctx, scope)
	if errors.Is(err, resident.ErrNotFound) {
		memory = resident.Memory{}
	} else if err != nil {
		return nil, err
	}
	result := memoryReadResult{Limits: t.Store.LimitsOrDefault(), Status: "ok"}
	if section == "all" || section == "core" {
		result.Core = memory.Core
	}
	if section == "all" || section == "normal" {
		result.Normal = memory.Normal
	}
	return memoryJSONResult(result)
}

func (t ResidentMemoryNormalTool) Name() string { return ResidentMemoryNormalToolName }
func (t ResidentMemoryNormalTool) Info() tool.Info {
	return memoryBuilder(t.Name(), normalDescription(t.Store), tool.RiskLow).Hidden().BuildInfo()
}
func (t ResidentMemoryNormalTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), normalDescription(t.Store), tool.RiskLow).
		String("action", "操作类型：append、write 或 delete。", tool.Required()).
		String("content", "append/write 时使用。write 会覆盖完整 normal，使用前必须先调用 resident_memory_read 读取 normal 或 all。delete 不需要 content。").
		BuildSchema()
}
func (t ResidentMemoryNormalTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	scope, err := memoryScope(ctx, t.Store)
	if err != nil {
		return nil, err
	}
	var args memoryNormalArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "append", "add":
		if args.Content == nil || strings.TrimSpace(*args.Content) == "" {
			return nil, fmt.Errorf("normal content is required")
		}
		if err := t.Store.AppendNormal(ctx, scope, *args.Content); err != nil {
			return nil, err
		}
		return &tool.Result{Content: "已追加普通常驻记忆。"}, nil
	case "write", "set", "update":
		if args.Content == nil {
			return nil, fmt.Errorf("normal content is required")
		}
		if err := t.Store.WriteNormal(ctx, scope, *args.Content); err != nil {
			return nil, err
		}
		return &tool.Result{Content: "已更新普通常驻记忆。"}, nil
	case "delete", "clear":
		if err := t.Store.DeleteNormal(ctx, scope); err != nil {
			return nil, err
		}
		return &tool.Result{Content: "已清空普通常驻记忆。"}, nil
	default:
		return nil, fmt.Errorf("action must be append, write or delete")
	}
}

func (t ResidentMemoryCoreTool) Name() string { return ResidentMemoryCoreToolName }
func (t ResidentMemoryCoreTool) Info() tool.Info {
	return memoryBuilder(t.Name(), coreDescription(t.Store), tool.RiskHigh).Hidden().BuildInfo()
}
func (t ResidentMemoryCoreTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), coreDescription(t.Store), tool.RiskHigh).
		String("content", "完整 core 内容。允许空字符串，表示清空 core。", tool.Required()).
		BuildSchema()
}
func (t ResidentMemoryCoreTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	scope, err := memoryScope(ctx, t.Store)
	if err != nil {
		return nil, err
	}
	var args memoryCoreArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	if args.Content == nil {
		return nil, fmt.Errorf("core content is required")
	}
	if err := t.Store.WriteCore(ctx, scope, *args.Content); err != nil {
		return nil, err
	}
	return &tool.Result{Content: "已更新核心常驻记忆。"}, nil
}

func memoryBuilder(name, description string, risk tool.RiskLevel) *tool.Builder {
	return tool.NewBuilder(name).Description(description).Risk(risk).OwnerScoped()
}

func normalDescription(store *resident.Store) string {
	limits := memoryLimits(store)
	return fmt.Sprintf("修改当前用户在当前平台的普通常驻记忆。支持 append、write、delete。write 会覆盖完整 normal，使用前必须先调用 resident_memory_read。normal 上限 %d 字数或单词。%s", limits.Normal, residentMemoryWritingRule)
}

func coreDescription(store *resident.Store) string {
	limits := memoryLimits(store)
	return fmt.Sprintf("覆盖当前用户在当前平台的核心常驻记忆。仅当用户明确要求修改核心记忆时使用；必须先调用 resident_memory_read 读取完整 core，再写入完整 core。允许传空字符串清空 core。core 上限 %d 字数或单词。%s", limits.Core, residentMemoryWritingRule)
}

func memoryLimits(store *resident.Store) resident.Limits {
	if store == nil {
		return resident.Limits{Core: resident.DefaultCoreMaxUnits, Normal: resident.DefaultNormalMaxUnits}
	}
	return store.LimitsOrDefault()
}

func validateMemoryStore(store *resident.Store) error {
	if store == nil {
		return fmt.Errorf("resident memory store is not configured")
	}
	return nil
}

func memoryScope(ctx context.Context, store *resident.Store) (session.Scope, error) {
	if err := validateMemoryStore(store); err != nil {
		return session.Scope{}, err
	}
	actor, ok := security.ActorFromContext(ctx)
	if !ok {
		return session.Scope{}, fmt.Errorf("resident memory actor is not available")
	}
	return resident.ActorScope(actor), nil
}

func decodeMemoryArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse resident memory arguments: %w", err)
	}
	return nil
}

func memoryJSONResult(value any) (*tool.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Content: string(data)}, nil
}
