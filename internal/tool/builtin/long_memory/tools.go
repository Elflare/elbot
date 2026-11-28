// 定义长期记忆工具，解析参数并准备执行及确认预览。
package long_memory

import (
	"context"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	longMemoryToolName       = "long_memory"
	longMemorySearchToolName = "long_memory_search"
	longMemoryWriteToolName  = "long_memory_write"
)

type LongMemoryTool struct{}
type LongMemorySearchTool struct{ store *longMemoryStore }
type LongMemoryWriteTool struct{ store *longMemoryStore }

type longMemorySearchArgs struct {
	Keywords  string `json:"keywords"`
	Category  string `json:"category"`
	MatchMode string `json:"match_mode"`
	BriefOnly bool   `json:"brief_only"`
	Limit     int    `json:"limit"`
}

type longMemoryWriteArgs struct {
	Operation    string         `json:"operation"`
	ID           int64          `json:"id"`
	Category     string         `json:"category"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Keywords     string         `json:"keywords"`
	Content      string         `json:"content"`
	ContentEdits []fileops.Edit `json:"content_edits"`
}

func NewLongMemoryTools(rootDir string) []toolshare.ToolDefinition {
	store := newLongMemoryStore(rootDir)
	return []toolshare.ToolDefinition{
		(LongMemoryTool{}).Definition(),
		(LongMemorySearchTool{store: store}).Definition(),
		(LongMemoryWriteTool{store: store}).Definition(),
	}
}

func (t LongMemoryTool) Definition() toolshare.ToolDefinition {
	return longMemoryBuilder(longMemoryToolName, "全局长期记忆入口；检索→long_memory_search，写入→long_memory_write。").DependsOn(longMemorySearchToolName, longMemoryWriteToolName).Build(t.PrepareCall)
}
func (t LongMemoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(info.Description)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		result := &toolshare.Result{Content: strings.TrimSpace("long_memory 是长期记忆入口。请按需调用 long_memory_search 或 long_memory_write。long_memory_search 不传 keywords/category 时会返回分类目录。")}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, "long_memory guide returned", nil
	})}, nil
}

func (t LongMemorySearchTool) Definition() toolshare.ToolDefinition {
	builder := longMemoryBuilder(longMemorySearchToolName, "检索全局长期记忆；keywords和category均空→分类目录。").Risk(toolshare.RiskMedium).Hidden()

	return builder.
		String("keywords", "关键词，空白/逗号/换行分隔。").
		String("category", "分类过滤。").
		String("match_mode", "关键词组合，默认OR。", toolshare.Enum("AND", "OR")).
		Boolean("brief_only", "默认true→标题+摘要；false→含全文。").
		Integer("limit", "默认3，上限10。").Build(t.PrepareCall)
}
func (t LongMemorySearchTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	args := longMemorySearchArgs{BriefOnly: true}
	if err := lmDecodeArgs(req.Arguments, &args, info.Name); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	args.Keywords = strings.TrimSpace(args.Keywords)
	args.Category = cleanLongMemoryText(args.Category)
	args.MatchMode = normalizeLongMemoryMatchMode(args.MatchMode)
	args.Limit = normalizeLongMemoryLimit(args.Limit)
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "keywords", Label: "关键词", Value: args.Keywords},
		toolshare.ConfirmationField{Key: "category", Label: "分类", Value: args.Category},
		toolshare.ConfirmationField{Key: "match_mode", Label: "匹配方式", Value: args.MatchMode},
		toolshare.ConfirmationField{Key: "brief_only", Label: "仅摘要", Value: args.BriefOnly},
		toolshare.ConfirmationField{Key: "limit", Label: "条数", Value: args.Limit},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		content, err := t.store.search(ctx, args)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if content.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: content.Status, Message: content.Message}, content.Message, nil
		}
		result := &toolshare.Result{Content: strings.TrimSpace(content.Data)}
		audit := "operation=categories"
		if args.Keywords != "" || args.Category != "" {
			audit = fmt.Sprintf("keywords=%q category=%q", args.Keywords, args.Category)
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, audit, nil
	})}, nil
}

func (t LongMemoryWriteTool) Definition() toolshare.ToolDefinition {
	builder := longMemoryBuilder(longMemoryWriteToolName, "写全局长期记忆；先long_memory_search查重。save⇒category,title,summary,keywords,content必填；update/delete⇒id必填；update省略/\"\"→保持原值；delete=永久删除。").Risk(toolshare.RiskHigh).Hidden()

	return builder.
		String("operation", "写操作。", toolshare.Required(), toolshare.Enum("save", "update", "delete")).
		Integer("id", "记忆ID。").
		String("category", "分类，优先复用已有分类。").
		String("title", "标题。").
		String("summary", "摘要，建议≤50字。").
		String("keywords", "搜索词，空白/逗号/换行分隔。").
		String("content", "全文；update=整体替换，与content_edits至多传一个；delete忽略。").
		ObjectArray("content_edits", "仅update；与content至多传一个；定位基于原文，修改范围不重叠，插入点不在其内部。", fileops.EditOperationProperties(), []string{"operation"}).Build(t.PrepareCall)
}

func (t LongMemoryWriteTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args longMemoryWriteArgs
	if err := lmDecodeArgs(req.Arguments, &args, info.Name); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	assessment := toolshare.RiskAssessment{Level: info.Risk}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	switch operation {
	case "save":
		record := longMemoryRecord{Category: cleanLongMemoryText(args.Category), Title: cleanLongMemoryText(args.Title), Summary: cleanLongMemoryText(args.Summary), Keywords: strings.Join(splitLongMemoryKeywords(args.Keywords), " "), Content: strings.TrimSpace(args.Content)}
		details := []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "operation", Label: "操作", Value: "save"},
			toolshare.ConfirmationField{Key: "category", Label: "分类", Value: record.Category},
			toolshare.ConfirmationField{Key: "title", Label: "标题", Value: record.Title},
			toolshare.ConfirmationField{Key: "summary", Label: "摘要", Value: record.Summary},
			toolshare.ConfirmationField{Key: "keywords", Label: "关键词", Value: record.Keywords},
			toolshare.ConfirmationField{Key: "content", Label: "正文", Value: record.Content},
		)}
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
			saved, err := t.store.save(ctx, record)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if saved.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: saved.Status, Message: saved.Message}, saved.Message, nil
			}
			record := saved.Data
			result := &toolshare.Result{Content: strings.TrimSpace(fmt.Sprintf("记忆写入成功！\nID：%d\n分类：%s\n标题：%s\n文件：%s", record.ID, record.Category, record.Title, record.FilePath))}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		})}, nil
	case "update":
		prepared, err := t.store.previewUpdate(ctx, args)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if prepared.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
		}
		preview := prepared.Data
		details := longMemoryUpdateRiskDetails(preview)
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
			updated, err := t.store.update(ctx, preview)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if updated.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: updated.Status, Message: updated.Message}, updated.Message, nil
			}
			record := updated.Data
			result := &toolshare.Result{Changes: toolshare.FileChanges(preview.File.Preview().Diff), Content: strings.TrimSpace(fmt.Sprintf("ID：%d 更新成功！\n分类：%s\n标题：%s\n文件：%s", record.ID, record.Category, record.Title, record.FilePath))}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		})}, nil
	case "delete":
		if args.ID <= 0 {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "id is required"}, nil
		}
		t.store.mu.Lock()
		defer t.store.mu.Unlock()
		found, err := t.store.prepareRecordByIDLocked(ctx, args.ID)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if found.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: found.Status, Message: found.Message}, nil
		}
		record := found.Data
		target, err := filepath.EvalSymlinks(record.FilePath)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		data, err := os.ReadFile(record.FilePath)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		revision := fileops.ContentRevision(data)
		if parsed, err := parseLongMemoryText(string(data)); err == nil {
			record.Title, record.Category, record.Summary = parsed.Meta.Title, parsed.Meta.Category, parsed.Meta.Summary
		}
		details := []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "operation", Label: "操作", Value: "delete"},
			toolshare.ConfirmationField{Key: "id", Label: "ID", Value: record.ID},
			toolshare.ConfirmationField{Key: "title", Label: "标题", Value: record.Title},
			toolshare.ConfirmationField{Key: "category", Label: "分类", Value: record.Category},
			toolshare.ConfirmationField{Key: "summary", Label: "摘要", Value: record.Summary},
			toolshare.ConfirmationField{Key: "path", Label: "文件", Value: record.FilePath},
		)}
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
			path, err := t.store.delete(ctx, record, target, revision)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if path.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: path.Status, Message: path.Message}, path.Message, nil
			}
			result := &toolshare.Result{Content: strings.TrimSpace(fmt.Sprintf("记忆ID：%d已永久删除。\n文件：%s", record.ID, path.Data))}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
		})}, nil
	default:
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "operation must be save, update, or delete"}, nil
	}
}

func longMemoryUpdateRiskDetails(preview longMemoryUpdatePreview) []toolshare.ConfirmationDetailBlock {
	filePreview := preview.File.Preview()
	return []toolshare.ConfirmationDetailBlock{
		toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "operation", Label: "操作", Value: "update"},
			toolshare.ConfirmationField{Key: "id", Label: "ID", Value: preview.After.ID},
			toolshare.ConfirmationField{Key: "path", Label: "文件", Value: preview.After.FilePath},
		),
		toolshare.FileDiffBlock(filePreview.Diff),
	}
}

func longMemoryBuilder(name, description string) *toolshare.Builder {
	return toolshare.NewBuilder(name).Weavable().Description(description).Risk(toolshare.RiskMedium).SuperadminOnly()
}

func lmDecodeArgs(raw []byte, out any, name string) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse %s arguments: %w", name, err)
	}
	return nil
}
