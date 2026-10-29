// 定义插件运行时控制状态、构造参数及工作目录解析。

// Package runtime runs stateful hook.v2 processes. It deliberately lives under
// the Hook subsystem: a persistent hook is still a Hook, not another plugin
// dispatch path.
package runtime

import "elbot/internal/tool"

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
)

type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusRunning  Status = "running"
	StatusDegraded Status = "degraded"
	StatusStopping Status = "stopping"
	StatusStopped  Status = "stopped"
	StatusFailed   Status = "failed"
)

type Options struct {
	Media mediaAPI // Borrowed media center; runtime owns only its own leases.
	Tools interface {
		ToolInfo(context.Context, string) (tool.Info, bool, error)
		ToolSchema(context.Context, string) (llm.ToolSchema, bool, error)
		CallTool(context.Context, tool.CallRequest) (*tool.Result, string, error)
	} // Metadata, schema and invocation without exposing Tool objects.

	// Send keeps delivery receipts separate from the source-owned log outcome.
	Send      func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	SharedDir string
	Log       func(context.Context, events.LogRecord)          // Publishes the source-owned record.
	Notify    func(context.Context, events.Notification) error // Publishes the source-owned notice.
}

func resolveCwd(dir, cwd string) (string, error) {
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("runtime cwd must be relative to plugin directory")
	}
	path := filepath.Clean(filepath.Join(dir, cwd))
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime cwd escapes plugin directory")
	}
	return path, nil
}
