// 定义长期记忆工具，解析参数并准备执行及确认预览。
package long_memory

import (
	"context"
	"database/sql"
	"elbot/internal/fileops"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"errors"
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
	BriefOnly *bool  `json:"brief_only"`
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
func (t LongMemoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, info.Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		result := &toolshare.Result{Content: strings.TrimSpace("long_memory 是长期记忆入口。请按需调用 long_memory_search 或 long_memory_write。long_memory_search 不传 keywords/category 时会返回分类目录。")}
		return result, "long_memory guide returned", nil
	}), nil
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
func (t LongMemorySearchTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
	var args longMemorySearchArgs
	if err := lmDecodeArgs(req.Arguments, &args, info.Name); err != nil {
		return nil, err
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, info.Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		if strings.TrimSpace(args.Keywords) == "" && strings.TrimSpace(args.Category) == "" {
			content, err := t.store.categories(ctx)
			if err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: strings.TrimSpace(content)}
			return result, "operation=categories", nil
		}
		content, err := t.store.search(ctx, args)
		if err != nil {
			return nil, err.Error(), err
		}
		result := &toolshare.Result{Content: strings.TrimSpace(content)}
		return result, fmt.Sprintf("keywords=%q category=%q", args.Keywords, args.Category), nil
	}), nil
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

func (t LongMemoryWriteTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
	var args longMemoryWriteArgs
	if err := lmDecodeArgs(req.Arguments, &args, info.Name); err != nil {
		return nil, err
	}
	assessment := toolshare.RiskAssessment{Level: info.Risk}
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "save":
		record := longMemoryRecord{Category: cleanLongMemoryText(args.Category), Title: cleanLongMemoryText(args.Title), Summary: cleanLongMemoryText(args.Summary), Keywords: strings.Join(splitLongMemoryKeywords(args.Keywords), " "), Content: strings.TrimSpace(args.Content)}
		detail := fmt.Sprintf("操作：新建长期记忆\n分类：%s\n标题：%s\n摘要：%s\n关键词：%s\n正文：\n%s", record.Category, record.Title, record.Summary, record.Keywords, record.Content)
		return toolshare.NewPreparedCall(req, assessment, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
			record, err := t.store.save(ctx, record)
			if err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: strings.TrimSpace(fmt.Sprintf("记忆写入成功！\nID：%d\n分类：%s\n标题：%s\n文件：%s\n可通过 long_memory_search 检索，或用 long_memory_write 更新/删除。", record.ID, record.Category, record.Title, record.FilePath))}
			return result, result.Content, nil
		}), nil
	case "update":
		preview, err := t.store.previewUpdate(ctx, longMemoryUpdateArgsFromWrite(args))
		if err != nil {
			return nil, err
		}
		return toolshare.NewPreparedCall(req, assessment, longMemoryUpdateRiskDetail(args, preview), func(ctx context.Context) (*toolshare.Result, string, error) {
			record, err := t.store.update(ctx, preview)
			if err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: strings.TrimSpace(fmt.Sprintf("ID：%d 更新成功！\n分类：%s\n标题：%s\n文件：%s", record.ID, record.Category, record.Title, record.FilePath))}
			return result, result.Content, nil
		}), nil
	case "delete":
		if args.ID <= 0 {
			return nil, fmt.Errorf("id is required")
		}
		t.store.mu.Lock()
		defer t.store.mu.Unlock()
		if err := t.store.ensureLocked(ctx); err != nil {
			return nil, err
		}
		record, err := t.store.recordByIDLocked(ctx, args.ID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := t.store.syncLocked(ctx, true); err != nil {
				return nil, err
			}
			record, err = t.store.recordByIDLocked(ctx, args.ID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("未找到 ID 为 %d 的长期记忆", args.ID)
		}
		if err != nil {
			return nil, err
		}
		target, err := filepath.EvalSymlinks(record.FilePath)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(record.FilePath)
		if err != nil {
			return nil, err
		}
		revision := fileops.ContentRevision(data)
		if parsed, err := parseLongMemoryText(string(data)); err == nil {
			record.Title, record.Category, record.Summary = parsed.Meta.Title, parsed.Meta.Category, parsed.Meta.Summary
		}
		detail := fmt.Sprintf("操作：永久删除长期记忆\nID：%d\n标题：%s\n分类：%s\n摘要：%s\n文件：%s", record.ID, record.Title, record.Category, record.Summary, record.FilePath)
		return toolshare.NewPreparedCall(req, assessment, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
			path, err := t.store.delete(ctx, record, target, revision)
			if err != nil {
				return nil, err.Error(), err
			}
			result := &toolshare.Result{Content: strings.TrimSpace(fmt.Sprintf("记忆ID：%d已永久删除。\n文件：%s", record.ID, path))}
			return result, result.Content, nil
		}), nil
	default:
		return nil, fmt.Errorf("operation must be save, update, or delete")
	}
}

func longMemoryUpdateRiskDetail(args longMemoryWriteArgs, preview longMemoryUpdatePreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "操作：更新长期记忆\n")
	fmt.Fprintf(&b, "ID：%d\n", preview.After.ID)
	fmt.Fprintf(&b, "文件：%s\n", preview.After.FilePath)
	fmt.Fprintf(&b, "分类：%s\n", preview.After.Category)
	fmt.Fprintf(&b, "标题：%s\n", preview.After.Title)
	b.WriteString("模式：确认后写入；确认前已自动预检\n")
	if len(args.ContentEdits) > 0 {
		b.WriteString("正文编辑：content_edits 只作用于正文；所有目标均基于更新前正文解析\n")
	}
	if len(preview.Changes) > 0 {
		b.WriteString("字段变化：\n")
		for _, change := range preview.Changes {
			fmt.Fprintf(&b, "- %s\n", change)
		}
	}
	b.WriteString("预检 diff:\n")
	b.WriteString(preview.File.Preview().Diff)
	if !strings.HasSuffix(preview.File.Preview().Diff, "\n") {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func longMemoryUpdateArgsFromWrite(args longMemoryWriteArgs) longMemoryUpdateArgs {
	update := longMemoryUpdateArgs{ID: args.ID, ContentEdits: args.ContentEdits}
	if args.Category != "" {
		update.Category = &args.Category
	}
	if args.Title != "" {
		update.Title = &args.Title
	}
	if args.Summary != "" {
		update.Summary = &args.Summary
	}
	if args.Keywords != "" {
		update.Keywords = &args.Keywords
	}
	if args.Content != "" {
		update.Content = &args.Content
	}
	return update
}

func longMemoryBuilder(name, description string) *toolshare.Builder {
	return toolshare.NewBuilder(name).Description(description).Risk(toolshare.RiskMedium).Tags("agent").SuperadminOnly()
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
