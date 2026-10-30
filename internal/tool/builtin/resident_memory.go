// 实现常驻记忆工具，读取和维护常驻记忆内容。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
)

const (
	ResidentMemoryToolName       = "resident_memory"
	ResidentMemoryReadToolName   = "resident_memory_read"
	ResidentMemoryNormalToolName = "resident_memory_normal"
	ResidentMemoryCoreToolName   = "resident_memory_core"
	residentMemoryWritingRule    = "内容必须使用第三人称：当前用户称为“用户”，模型自身称为“assistant”；只记录背景信息，不记录对模型的指令"
)

// ResidentMemoryService 只操作当前调用者的常驻记忆，身份与存储范围由根部适配。
type ResidentMemoryService interface {
	Read(context.Context) (ResidentMemory, error)
	AppendNormal(context.Context, string) error
	WriteNormal(context.Context, string) error
	DeleteNormal(context.Context) error
	WriteCore(context.Context, string) error
	LimitsOrDefault() ResidentMemoryLimits
}

type ResidentMemory struct {
	Core   string // 核心记忆；未保存时为空。
	Normal string // 普通记忆；未保存时为空。
}

type ResidentMemoryLimits struct {
	Core   int // 核心记忆允许的最大字数或单词数。
	Normal int // 普通记忆允许的最大字数或单词数。
}

type ResidentMemoryTool struct{}

type ResidentMemoryReadTool struct {
	service ResidentMemoryService
}

type ResidentMemoryNormalTool struct {
	service ResidentMemoryService
}

type ResidentMemoryCoreTool struct {
	service ResidentMemoryService
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
	Core   string               `json:"core,omitempty"`
	Normal string               `json:"normal,omitempty"`
	Limits ResidentMemoryLimits `json:"limits"`
	Status string               `json:"status"`
}

func NewResidentMemoryTools(service ResidentMemoryService) []toolshare.Tool {
	return []toolshare.Tool{ResidentMemoryTool{}, ResidentMemoryReadTool{service: service}, ResidentMemoryNormalTool{service: service}, ResidentMemoryCoreTool{service: service}}
}

func (t ResidentMemoryTool) Name() string { return ResidentMemoryToolName }
func (t ResidentMemoryTool) Info() toolshare.Info {
	return toolshare.NewBuilder(t.Name()).
		Description("管理当前用户在当前平台的常驻记忆。本工具仅为入口。").
		Risk(toolshare.RiskLow).
		DependsOn(ResidentMemoryReadToolName, ResidentMemoryNormalToolName, ResidentMemoryCoreToolName).
		BuildInfo()
}
func (t ResidentMemoryTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(t.Name()).Description(t.Info().Description).BuildSchema()
}
func (t ResidentMemoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		result := &toolshare.Result{Content: "resident_memory 是常驻记忆管理入口。请调用 resident_memory_read、resident_memory_normal 或 resident_memory_core。"}
		return result, "resident_memory guide returned", nil
	}), nil
}

func (t ResidentMemoryReadTool) Name() string { return ResidentMemoryReadToolName }
func (t ResidentMemoryReadTool) Info() toolshare.Info {
	return memoryBuilder(t.Name(), "读取当前用户在当前平台的常驻记忆。section 可为 all、core 或 normal，默认 all。", toolshare.RiskLow).Hidden().BuildInfo()
}
func (t ResidentMemoryReadTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), "读取当前用户在当前平台的常驻记忆。section 可为 all、core 或 normal，默认 all。", toolshare.RiskLow).
		String("section", "读取范围：all、core 或 normal。默认 all。").
		BuildSchema()
}
func (t ResidentMemoryReadTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
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
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		memory, err := t.service.Read(ctx)
		if err != nil {
			return nil, err.Error(), err
		}
		data := memoryReadResult{Limits: t.service.LimitsOrDefault(), Status: "ok"}
		if section == "all" || section == "core" {
			data.Core = memory.Core
		}
		if section == "all" || section == "normal" {
			data.Normal = memory.Normal
		}
		logContent = fmt.Sprintf("section=%s core_bytes=%d normal_bytes=%d", section, len(data.Core), len(data.Normal))
		result, callErr := memoryJSONResult(data)
		if callErr != nil {
			logContent = callErr.Error()
		}
		return result, logContent, callErr
	}), nil
}

func (t ResidentMemoryNormalTool) Name() string { return ResidentMemoryNormalToolName }
func (t ResidentMemoryNormalTool) Info() toolshare.Info {
	return memoryBuilder(t.Name(), normalDescription(t.service), toolshare.RiskLow).Hidden().BuildInfo()
}
func (t ResidentMemoryNormalTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), normalDescription(t.service), toolshare.RiskLow).
		String("action", "操作类型：append、write 或 delete。", toolshare.Required()).
		String("content", "append/write 时使用。write 会覆盖完整 normal，使用前必须先调用 resident_memory_read 读取 normal 或 all。delete 不需要 content。").
		BuildSchema()
}
func (t ResidentMemoryNormalTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args memoryNormalArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("操作：%s\n范围：普通常驻记忆", args.Action), func(ctx context.Context) (*toolshare.Result, string, error) {
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "append", "add":
			if args.Content == nil {
				callErr := fmt.Errorf("normal content is required")
				return nil, callErr.Error(), callErr
			}
			if err := t.service.AppendNormal(ctx, *args.Content); err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: "已追加普通常驻记忆。"}
			return result, result.Content, nil
		case "write", "set", "update":
			if args.Content == nil {
				callErr := fmt.Errorf("normal content is required")
				return nil, callErr.Error(), callErr
			}
			if err := t.service.WriteNormal(ctx, *args.Content); err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: "已更新普通常驻记忆。"}
			return result, result.Content, nil
		case "delete", "clear":
			if err := t.service.DeleteNormal(ctx); err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: "已清空普通常驻记忆。"}
			return result, result.Content, nil
		default:
			callErr := fmt.Errorf("action must be append, write or delete")
			return nil, callErr.Error(), callErr
		}
	}), nil
}

func (t ResidentMemoryCoreTool) Name() string { return ResidentMemoryCoreToolName }
func (t ResidentMemoryCoreTool) Info() toolshare.Info {
	return memoryBuilder(t.Name(), coreDescription(t.service), toolshare.RiskHigh).Hidden().BuildInfo()
}
func (t ResidentMemoryCoreTool) Schema() llm.ToolSchema {
	return memoryBuilder(t.Name(), coreDescription(t.service), toolshare.RiskHigh).
		String("content", "完整 core 内容。允许空字符串，表示清空 core。", toolshare.Required()).
		BuildSchema()
}
func (t ResidentMemoryCoreTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args memoryCoreArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	if args.Content == nil {
		return nil, fmt.Errorf("core content is required")
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("覆盖核心常驻记忆：\n%s", *args.Content), func(ctx context.Context) (*toolshare.Result, string, error) {
		if err := t.service.WriteCore(ctx, *args.Content); err != nil {
			return nil, err.Error(), err
		}
		result := &toolshare.Result{Content: "已更新核心常驻记忆。"}
		return result, result.Content, nil
	}), nil
}

func memoryBuilder(name, description string, risk toolshare.RiskLevel) *toolshare.Builder {
	return toolshare.NewBuilder(name).Description(description).Risk(risk).OwnerScoped()
}

func normalDescription(service ResidentMemoryService) string {
	limits := service.LimitsOrDefault()
	return fmt.Sprintf("修改当前用户在当前平台的普通常驻记忆。支持 append、write、delete。write 会覆盖完整 normal，使用前必须先调用 resident_memory_read。normal 上限 %d 字数或单词。%s", limits.Normal, residentMemoryWritingRule)
}

func coreDescription(service ResidentMemoryService) string {
	limits := service.LimitsOrDefault()
	return fmt.Sprintf("覆盖当前用户在当前平台的核心常驻记忆。仅当用户明确要求修改核心记忆时使用；必须先调用 resident_memory_read 读取完整 core，再写入完整 core。允许传空字符串清空 core。core 上限 %d 字数或单词。%s", limits.Core, residentMemoryWritingRule)
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

func memoryJSONResult(value any) (*toolshare.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &toolshare.Result{Content: string(data)}, nil
}
