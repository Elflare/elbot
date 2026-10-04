package builtin

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

type RollbackFileTool struct {
	Service *tool.FileRollbackService
}

type rollbackFileArgs struct {
	Path string `json:"path"`
}

func NewRollbackFileTool(service *tool.FileRollbackService) RollbackFileTool {
	return RollbackFileTool{Service: service}
}

func (RollbackFileTool) Name() string    { return "rollback_file" }
func (RollbackFileTool) Info() tool.Info { return rollbackFileBuilder().BuildInfo() }
func (RollbackFileTool) Schema() llm.ToolSchema {
	schema := rollbackFileBuilder().BuildSchema()
	schema.Function.Parameters["additionalProperties"] = false
	return schema
}

func rollbackFileBuilder() *tool.Builder {
	return tool.NewBuilder("rollback_file").
		Description("撤销指定文件在当前会话中最近一次成功的 edit_file 修改。已有文件恢复编辑前内容，新建文件删除。文件此后发生变化时拒绝撤销；成功后消耗备份，不支持连续撤销或重做。备份仅保存在内存中，切换会话、重启或容量淘汰后失效。不支持撤销 shell 或外部程序的修改。").
		Hidden().
		SuperadminOnly().
		ForegroundOnly().
		Risk(tool.RiskHigh).
		Tags("files").
		DependsOn("workspace").
		String("path", "要撤销最近一次编辑的文件路径；相对路径基于当前 workspace，也支持绝对路径。", tool.Required())
}

func decodeRollbackArgs(req tool.CallRequest) (rollbackFileArgs, error) {
	var args rollbackFileArgs
	if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
		return args, fmt.Errorf("parse rollback_file arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return args, fmt.Errorf("path is required")
	}
	return args, nil
}

func (t RollbackFileTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	args, err := decodeRollbackArgs(req)
	if err != nil {
		return err
	}
	_, err = t.Service.Preview(ctx, args.Path)
	return err
}

func (t RollbackFileTool) RiskDetail(ctx context.Context, req tool.CallRequest) (string, error) {
	args, err := decodeRollbackArgs(req)
	if err != nil {
		return "", err
	}
	preview, err := t.Service.Preview(ctx, args.Path)
	if err != nil {
		return "", err
	}
	action := "恢复编辑前内容"
	if preview.Created {
		action = "删除本次新建文件（保留父目录）"
	}
	return fmt.Sprintf("文件：%s\n操作：%s\n预检 diff:\n%s", preview.Path, action, preview.Diff), nil
}

func (t RollbackFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := decodeRollbackArgs(req)
	if err != nil {
		return nil, err
	}
	result, err := t.Service.Rollback(ctx, args.Path, 0)
	if err != nil {
		return nil, err
	}
	action := "restored"
	if result.Deleted {
		action = "deleted_created_file"
	}
	return &tool.Result{Content: fmt.Sprintf("rolled_back: %s\naction: %s\nrevision: %s", result.Path, action, result.Revision)}, nil
}
