package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

const maxWorkspaceAgentFileSize = 64 * 1024

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
		content := fmt.Sprintf("workspace set.\ncurrent workspace: %s", dir)
		markNotice := false
		if noticeStore, ok := store.(tool.WorkspaceAgentNoticeStore); ok {
			instructions, mark, err := workspaceAgentInstructions(ctx, noticeStore, dir)
			if err != nil {
				return nil, err
			}
			markNotice = mark
			if instructions != "" {
				content += "\n\n" + instructions
			}
			if err := noticeStore.SetWorkspaceDirWithAgentNotice(ctx, dir, markNotice); err != nil {
				return nil, fmt.Errorf("set workspace: %w", err)
			}
			return &tool.Result{Content: content}, nil
		}
		if err := store.SetWorkspaceDir(ctx, dir); err != nil {
			return nil, fmt.Errorf("set workspace: %w", err)
		}
		return &tool.Result{Content: content}, nil
	}
	dir, err := tool.CurrentWorkspaceDir(ctx)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Content: fmt.Sprintf("current workspace: %s", dir)}, nil
}

func workspaceAgentInstructions(ctx context.Context, noticeStore tool.WorkspaceAgentNoticeStore, dir string) (string, bool, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	loaded, err := noticeStore.HasWorkspaceAgentNoticeDir(ctx, dir)
	if err != nil {
		return "", false, fmt.Errorf("load workspace instructions notice: %w", err)
	}
	if loaded {
		return "", false, nil
	}
	path, name, ok, err := findWorkspaceAgentFile(dir)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("stat workspace instructions %s: %w", name, err)
	}
	if info.Size() > maxWorkspaceAgentFileSize {
		return fmt.Sprintf("workspace instructions file is larger than 64 KiB and was not loaded: %s\nPlease tell the user to shorten or split it before switching workspace again.", name), false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read workspace instructions %s: %w", name, err)
	}
	return fmt.Sprintf("Loaded workspace instructions from %s:\n%s", name, strings.TrimRight(string(data), "\r\n")), true, nil
}

func findWorkspaceAgentFile(dir string) (string, string, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false, fmt.Errorf("read workspace directory: %w", err)
	}
	for _, base := range []string{"AGENTS", "AGENT"} {
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || strings.TrimSuffix(name, filepath.Ext(name)) != base || !strings.EqualFold(filepath.Ext(name), ".md") {
				continue
			}
			return filepath.Join(dir, name), name, true, nil
		}
	}
	return "", "", false, nil
}
