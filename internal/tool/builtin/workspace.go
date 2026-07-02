package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

type WorkspaceTool struct{}

type workspaceArgs struct {
	Path  string `json:"path"`
	Reset bool   `json:"reset"`
}

func NewWorkspaceTool() WorkspaceTool {
	return WorkspaceTool{}
}

func (WorkspaceTool) Name() string { return "workspace" }

func (WorkspaceTool) Info() tool.Info { return workspaceBuilder().BuildInfo() }

func (WorkspaceTool) Schema() llm.ToolSchema { return workspaceBuilder().BuildSchema() }

func workspaceBuilder() *tool.Builder {
	return tool.NewBuilder("workspace").
		Description("设置当前 Session 的共享工作目录。所有需要路径的工具会基于该目录解析相对路径。").
		Risk(tool.RiskLow).
		SuperadminOnly().
		Tags("agent", "files").
		ForegroundOnly().
		String("path", "要设置为 workspace 的已有目录路径。").
		Boolean("reset", "为 true 时重置为默认工作目录。")
}

func (WorkspaceTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args workspaceArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse workspace arguments: %w", err)
		}
	}
	store, hasStore := tool.WorkspaceStoreFromContext(ctx)
	if args.Reset {
		if hasStore {
			if err := store.ClearWorkspaceDir(ctx); err != nil {
				return nil, fmt.Errorf("reset workspace: %w", err)
			}
		}
		dir, err := tool.CurrentWorkspaceDir(ctx)
		if err != nil {
			return nil, err
		}
		return &tool.Result{Content: fmt.Sprintf("workspace reset.\ncurrent workspace: %s", dir)}, nil
	}
	if path := strings.TrimSpace(args.Path); path != "" {
		if !hasStore {
			return nil, fmt.Errorf("workspace is unavailable in this context")
		}
		dir, err := tool.ValidateWorkspaceDir(path)
		if err != nil {
			return nil, err
		}
		if err := store.SetWorkspaceDir(ctx, dir); err != nil {
			return nil, fmt.Errorf("set workspace: %w", err)
		}
		return &tool.Result{Content: fmt.Sprintf("workspace set.\ncurrent workspace: %s", dir)}, nil
	}
	dir, err := tool.CurrentWorkspaceDir(ctx)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Content: fmt.Sprintf("current workspace: %s", dir)}, nil
}
