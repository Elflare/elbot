// 实现文件撤销工具，准备并提交文件回滚。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/fileops"
)

type RollbackFileTool struct {
	Service *fileops.Service
}

type rollbackFileArgs struct {
	Path string `json:"path"`
}

func (t RollbackFileTool) Definition() toolshare.ToolDefinition {
	builder := rollbackFileBuilder()

	schema := builder.BuildSchema()
	schema.Parameters["additionalProperties"] = false
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
}

func rollbackFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("rollback_file").
		Description("撤销当前Session最近一次edit_file：已有→恢复，新建→删除；后续变化→拒绝；一次性，无redo；切换Session/重启/淘汰后失效；不含shell或外部修改。").
		Hidden().
		SuperadminOnly().
		ForegroundOnly().
		Risk(toolshare.RiskHigh).
		Tags("files").
		DependsOn("workspace").
		String("path", "相对workspace或绝对文件路径。", toolshare.Required())
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

func (t RollbackFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
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
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
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
