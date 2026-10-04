package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceContextKey struct{}

const absolutePathWorkspaceWarning = "使用了绝对路径，不会改变 workspace。若多次在该目录工作，建议先使用 workspace 工具切换。"

type WorkspaceStore interface {
	GetWorkspaceDir(ctx context.Context) (string, error)
	SetWorkspaceDir(ctx context.Context, dir string) error
	ClearWorkspaceDir(ctx context.Context) error
}

type WorkspaceAgentNoticeStore interface {
	HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error)
	MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error
	SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error
	ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error
}

type PathResolveOptions struct {
	AllowCreate    bool
	AllowDirectory bool
}

type ResolvedPath struct {
	Path     string
	BaseDir  string
	WasAbs   bool
	Warnings []string
}

func WithWorkspaceStore(ctx context.Context, store WorkspaceStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, workspaceContextKey{}, store)
}

func WorkspaceStoreFromContext(ctx context.Context) (WorkspaceStore, bool) {
	store, ok := ctx.Value(workspaceContextKey{}).(WorkspaceStore)
	return store, ok
}

func CurrentWorkspaceDir(ctx context.Context) (string, error) {
	if store, ok := WorkspaceStoreFromContext(ctx); ok {
		dir, err := store.GetWorkspaceDir(ctx)
		if err != nil {
			return "", fmt.Errorf("load workspace: %w", err)
		}
		if strings.TrimSpace(dir) != "" {
			return ValidateWorkspaceDir(dir)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(cwd), nil
}
