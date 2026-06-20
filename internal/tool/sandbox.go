package tool

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

type sandboxContextKey struct{}

type BackgroundKind string

const (
	BackgroundKindCron  BackgroundKind = "cron"
	BackgroundKindElnis BackgroundKind = "elnis"
)

// SandboxContext 描述本次工具执行的轻量沙盒运行态，只随 context 传播，不持久化。
type SandboxContext struct {
	Root           string
	Dir            string
	ArtifactDir    string
	Background     bool
	BackgroundKind BackgroundKind
}

func WithSandboxContext(ctx context.Context, sandbox SandboxContext) context.Context {
	return context.WithValue(ctx, sandboxContextKey{}, sandbox)
}

func SandboxContextFromContext(ctx context.Context) (SandboxContext, bool) {
	sandbox, ok := ctx.Value(sandboxContextKey{}).(SandboxContext)
	return sandbox, ok
}

func BackgroundPathInstruction() string {
	return "所有路径参数必须使用相对路径，基于当前任务工作目录解析；不要使用绝对路径、~、.. 或 cd。"
}

func ResolveSandboxRelativePath(sandbox SandboxContext, rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(rawPath, "~") {
		return "", fmt.Errorf("background path must be relative and must not use ~")
	}
	slashPath := strings.ReplaceAll(rawPath, "\\", "/")
	if strings.HasPrefix(slashPath, "/") || filepath.IsAbs(rawPath) || isWindowsAbsPath(slashPath) {
		return "", fmt.Errorf("background path must be relative")
	}
	cleanSlash := path.Clean(slashPath)
	if cleanSlash == ".." || strings.HasPrefix(cleanSlash, "../") {
		return "", fmt.Errorf("background path must not escape task workspace")
	}
	root := strings.TrimSpace(sandbox.Dir)
	if root == "" {
		root = strings.TrimSpace(sandbox.Root)
	}
	if root == "" {
		return "", fmt.Errorf("background sandbox is not configured")
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve background sandbox: %w", err)
	}
	candidateAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(cleanSlash)))
	if err != nil {
		return "", fmt.Errorf("resolve background path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("check background path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("background path must not escape task workspace")
	}
	return candidateAbs, nil
}

func isWindowsAbsPath(value string) bool {
	return len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && value[2] == '/'
}

func isASCIIAlpha(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}
