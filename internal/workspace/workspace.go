// 定义工作目录契约与路径范围，并选择当前调用使用的工作目录。
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathScope 固定本次调用的工作目录及路径限制，不携带后台来源或修改 Session 状态。
type PathScope struct {
	// Dir 是本次调用固定的工作目录；未指定且不受限时使用 Session 工作目录。
	Dir string
	// Restricted 表示路径必须限制在 Dir 内；受限范围必须指定目录。
	Restricted bool
}

// WorkspaceStore 由 Session 侧实现，提供持久化工作目录的读写能力。
type WorkspaceStore interface {
	GetWorkspaceDir(ctx context.Context) (string, error)
	SetWorkspaceDir(ctx context.Context, dir string) error
	ClearWorkspaceDir(ctx context.Context) error
	// ClearWorkspaceDirIfMatches atomically clears only the expected saved path.
	ClearWorkspaceDirIfMatches(ctx context.Context, dir string) (bool, error)
}

// WorkspaceAgentNoticeStore 将目录切换与说明文件的提示记录一起提交。
type WorkspaceAgentNoticeStore interface {
	HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error)
	MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error
	SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error
	ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error
}

type PathResolveOptions struct {
	// AllowCreate 允许目标尚不存在，不负责创建目标或父目录。
	AllowCreate bool
	// AllowDirectory 允许目标为目录。
	AllowDirectory bool
	// RequireRegularFile 要求已存在的目标为普通文件；不改变 AllowCreate 的创建许可。
	RequireRegularFile bool
}

type ResolvedPath struct {
	// Path 是解析后的本地目标路径。
	Path string
	// BaseDir 是本次解析使用的工作目录。
	BaseDir string
	// WasAbs 表示输入经家目录展开和平台格式转换后为绝对路径。
	WasAbs bool
}

// CurrentWorkspaceDir 优先使用调用级目录，其次读取 Session，最后回退进程目录。
// 固定目录不在此创建或因缺失而回退，目录准备由建立调用范围的业务负责。
func CurrentWorkspaceDir(ctx context.Context) (string, error) {
	if scope, ok := PathScopeFromContext(ctx); ok {
		if dir := strings.TrimSpace(scope.Dir); dir != "" {
			return filepath.Abs(dir)
		}
		if scope.Restricted {
			return "", fmt.Errorf("restricted workspace directory is not configured")
		}
	}
	store, ok := WorkspaceStoreFromContext(ctx)
	for {
		var dir string
		if ok {
			var err error
			dir, err = store.GetWorkspaceDir(ctx)
			if err != nil {
				return "", fmt.Errorf("load workspace: %w", err)
			}
			if strings.TrimSpace(dir) != "" {
				resolved, err := ValidateWorkspaceDir(dir)
				if err == nil {
					return resolved, nil
				}
				if !errors.Is(err, os.ErrNotExist) {
					return "", err
				}
			}
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
		if strings.TrimSpace(dir) != "" {
			cleared, err := store.ClearWorkspaceDirIfMatches(ctx, dir)
			if err != nil {
				return "", fmt.Errorf("clear missing workspace: %w", err)
			}
			if !cleared {
				continue
			}
		}
		return filepath.Clean(cwd), nil
	}
}
