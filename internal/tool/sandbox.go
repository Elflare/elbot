package tool

import "context"

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
