// 实现文件撤销工具，准备并提交文件回滚。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/fileops"
	"elbot/internal/llm"
)

type RollbackFileTool struct {
	Service *fileops.Service
}

type rollbackFileArgs struct {
	Path string `json:"path"`
}

func NewRollbackFileTool(service *fileops.Service) RollbackFileTool {
	return RollbackFileTool{Service: service}
}

func (RollbackFileTool) Name() string         { return "rollback_file" }
func (RollbackFileTool) Info() toolshare.Info { return rollbackFileBuilder().BuildInfo() }
func (RollbackFileTool) Schema() llm.ToolSchema {
	schema := rollbackFileBuilder().BuildSchema()
	schema.Parameters["additionalProperties"] = false
	return schema
}

func rollbackFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("rollback_file").
		Description("撤销指定文件在当前会话中最近一次成功的 edit_file 修改。已有文件恢复编辑前内容，新建文件删除。文件此后发生变化时拒绝撤销；成功后消耗备份，不支持连续撤销或重做。备份仅保存在内存中，切换会话、重启或容量淘汰后失效。不支持撤销 shell 或外部程序的修改。").
		Hidden().
		SuperadminOnly().
		ForegroundOnly().
		Risk(toolshare.RiskHigh).
		Tags("files").
		DependsOn("workspace").
		String("path", "要撤销最近一次编辑的文件路径；相对路径基于当前 workspace，也支持绝对路径。", toolshare.Required())
}

func decodeRollbackArgs(req toolshare.CallRequest) (rollbackFileArgs, error) {
	var args rollbackFileArgs
	if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
		return args, fmt.Errorf("parse rollback_file arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return args, fmt.Errorf("path is required")
	}
	return args, nil
}

func (t RollbackFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	args, err := decodeRollbackArgs(req)
	if err != nil {
		return nil, err
	}
	prepared, err := t.Service.PrepareRollback(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	preview := prepared.Preview()
	action := "恢复编辑前内容"
	if preview.Created {
		action = "删除本次新建文件（保留父目录）"
	}
	detail := fmt.Sprintf("文件：%s\n操作：%s\n预检 diff:\n%s", preview.Path, action, preview.Diff)
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.call(ctx, prepared)
	}), nil
}

func (t RollbackFileTool) call(ctx context.Context, prepared *fileops.PreparedRollback) (*toolshare.Result, string, error) {
	result, err := prepared.Commit(ctx)
	if err != nil {
		return nil, err.Error(), err
	}
	action := "restored"
	if result.Deleted {
		action = "deleted_created_file"
	}
	content := fmt.Sprintf("rolled_back: %s\naction: %s\nrevision: %s", result.Path, action, result.Revision)
	return &toolshare.Result{Content: content}, content, nil
}
