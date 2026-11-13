// 实现常驻记忆工具，读取和维护常驻记忆内容。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/memory/resident"
	"elbot/internal/operationresult"
	"encoding/json"
	"fmt"
	"strings"
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
	Read(context.Context) (operationresult.Result[resident.Memory], error)
	AppendNormal(context.Context, string) (operationresult.Result[struct{}], error)
	WriteNormal(context.Context, string) (operationresult.Result[struct{}], error)
	DeleteNormal(context.Context) (operationresult.Result[struct{}], error)
	WriteCore(context.Context, string) (operationresult.Result[struct{}], error)
	LimitsOrDefault() resident.Limits
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

func NewResidentMemoryTools(service ResidentMemoryService) []toolshare.ToolDefinition {
	return []toolshare.ToolDefinition{(ResidentMemoryTool{}).Definition(), (ResidentMemoryReadTool{service: service}).Definition(), (ResidentMemoryNormalTool{service: service}).Definition(), (ResidentMemoryCoreTool{service: service}).Definition()}
}

func (t ResidentMemoryTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(ResidentMemoryToolName).
		Description("当前用户/平台的常驻记忆入口；读→resident_memory_read，普通写→resident_memory_normal，核心写→resident_memory_core。").
		Risk(toolshare.RiskLow).
		DependsOn(ResidentMemoryReadToolName, ResidentMemoryNormalToolName, ResidentMemoryCoreToolName)
	return builder.Build(t.PrepareCall)
}
func (t ResidentMemoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(info.Description)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		result := &toolshare.Result{Content: "resident_memory 是常驻记忆管理入口。请调用 resident_memory_read、resident_memory_normal 或 resident_memory_core。"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, "resident_memory guide returned", nil
	})}, nil
}

func (t ResidentMemoryReadTool) Definition() toolshare.ToolDefinition {
	builder := memoryBuilder(ResidentMemoryReadToolName, "读当前用户/平台的常驻记忆。", toolshare.RiskLow).Hidden()

	return builder.
		String("section", "读取范围，默认all。", toolshare.Enum("all", "core", "normal")).Build(t.PrepareCall)
}
func (t ResidentMemoryReadTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args memoryReadArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	section := strings.ToLower(strings.TrimSpace(args.Section))
	if section == "" {
		section = "all"
	}
	if section != "all" && section != "core" && section != "normal" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "section must be all, core or normal"}, nil
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(info.Description)}, func(ctx context.Context) (result operationresult.Result[*toolshare.Result], logContent string, _ error) {
		loaded, err := t.service.Read(ctx)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if loaded.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: loaded.Status, Message: loaded.Message}, loaded.Message, nil
		}
		memory := loaded.Data
		limits := t.service.LimitsOrDefault()
		var content string
		switch section {
		case "core":
			content = fmt.Sprintf("core(limits:%d)：%s", limits.Core, memory.Core)
		case "normal":
			content = fmt.Sprintf("normal(limits:%d)：%s", limits.Normal, memory.Normal)
		case "all":
			content = fmt.Sprintf(
				"core(limits:%d)：%s\nnormal(limits:%d)：%s",
				limits.Core, memory.Core,
				limits.Normal, memory.Normal,
			)
		}
		logContent = fmt.Sprintf("section=%s core_bytes=%d normal_bytes=%d", section, len(memory.Core), len(memory.Normal))
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: content}}, logContent, nil
	})}, nil
}

func (t ResidentMemoryNormalTool) Definition() toolshare.ToolDefinition {
	builder := memoryBuilder(ResidentMemoryNormalToolName, normalDescription(t.service), toolshare.RiskLow).Hidden()

	return builder.
		String("action", "append=追加；write=整体覆盖；delete=清空normal。", toolshare.Required(), toolshare.Enum("append", "write", "delete")).
		String("content", "append/write必填；write先用resident_memory_read读normal/all；delete忽略。").Build(t.PrepareCall)
}
func (t ResidentMemoryNormalTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args memoryNormalArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action != "append" && action != "write" && action != "delete" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "action must be append, write or delete"}, nil
	}
	if action != "delete" && args.Content == nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "normal content is required"}, nil
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("操作：%s\n范围：普通常驻记忆", args.Action))}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		switch action {
		case "append":
			written, err := t.service.AppendNormal(ctx, *args.Content)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if written.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: written.Status, Message: written.Message}, written.Message, nil
			}
			result := &toolshare.Result{Content: "已追加普通常驻记忆。"}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		case "write":
			written, err := t.service.WriteNormal(ctx, *args.Content)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if written.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: written.Status, Message: written.Message}, written.Message, nil
			}
			result := &toolshare.Result{Content: "已更新普通常驻记忆。"}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		case "delete":
			written, err := t.service.DeleteNormal(ctx)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if written.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: written.Status, Message: written.Message}, written.Message, nil
			}
			result := &toolshare.Result{Content: "已清空普通常驻记忆。"}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		default:
			return operationresult.Result[*toolshare.Result]{}, "", fmt.Errorf("unreachable memory action %q", action)
		}
	})}, nil
}

func (t ResidentMemoryCoreTool) Definition() toolshare.ToolDefinition {
	builder := memoryBuilder(ResidentMemoryCoreToolName, coreDescription(t.service), toolshare.RiskHigh).Hidden()

	return builder.
		String("content", "完整core；\"\"→清空。", toolshare.Required()).Build(t.PrepareCall)
}
func (t ResidentMemoryCoreTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args memoryCoreArgs
	if err := decodeMemoryArgs(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if args.Content == nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "core content is required"}, nil
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("覆盖核心常驻记忆：\n%s", *args.Content))}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		written, err := t.service.WriteCore(ctx, *args.Content)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if written.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: written.Status, Message: written.Message}, written.Message, nil
		}
		result := &toolshare.Result{Content: "已更新核心常驻记忆。"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
	})}, nil
}

func memoryBuilder(name, description string, risk toolshare.RiskLevel) *toolshare.Builder {
	return toolshare.NewBuilder(name).Description(description).Risk(risk).OwnerScoped()
}

func normalDescription(service ResidentMemoryService) string {
	limits := service.LimitsOrDefault()
	return fmt.Sprintf("写当前用户/平台的normal；上限%d单位(CJK字符各1，其余连续字母/数字各1)。%s", limits.Normal, residentMemoryWritingRule)
}

func coreDescription(service ResidentMemoryService) string {
	limits := service.LimitsOrDefault()
	return fmt.Sprintf("重要内容可写入当前用户/平台的core；先resident_memory_read读完整core→整体覆盖。上限%d单位(CJK字符各1，其余连续字母/数字各1)。%s", limits.Core, residentMemoryWritingRule)
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
