// 承接 Tool 对外能力入口，转接内部服务。
package tool

import (
	"context"
	"sync"

	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/session"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/share"
	toolstate "elbot/internal/tool/state"
)

// Supervisor 只持有内部组件并转接对外能力；装配和生命周期分别由 wiring.go、lifecycle.go 负责。
type Supervisor struct {
	catalog   *Runtime
	engine    *execution.Manager
	preloader *preload.PreloadService
	state     *toolstate.StateService
	ctx       context.Context    // Tool 生命周期上下文。
	cancel    context.CancelFunc // 由生命周期入口取消后台工作。
	mu        sync.Mutex         // 保护启动、关闭状态。
	started   bool               // 标记已启动。
	closed    bool               // 标记已进入关闭阶段。
	done      chan struct{}      // 生命周期退出通知。
}

func (m *Supervisor) QueryTools(ctx context.Context, query share.ToolQuery) ([]share.Info, error) {
	return m.catalog.Registry.QueryToolInfos(ctx, query)
}

func (m *Supervisor) ToolTags(ctx context.Context) ([]string, error) {
	return m.preloader.Tags(ctx)
}

func (m *Supervisor) ToolNamesByTag(ctx context.Context, tag string, query share.ToolQuery) ([]string, error) {
	return m.preloader.ToolNamesByTag(ctx, tag, query)
}

func (m *Supervisor) DiscoveredToolsForSession(ctx context.Context, id string, background bool) ([]share.DiscoveredTool, events.LogResult, error) {
	return m.state.DiscoveredToolsForSession(ctx, id, background)
}

func (m *Supervisor) ToolInfos(ctx context.Context, view share.Context) ([]share.Info, error) {
	return m.engine.ToolInfos(ctx, view)
}
func (m *Supervisor) Schemas(ctx context.Context, view share.Context, discovered []share.DiscoveredTool) ([]llm.ToolSchema, error) {
	return m.engine.Schemas(ctx, view, discovered)
}
func (m *Supervisor) Run(ctx context.Context, deps share.RunnerDeps, req share.RunRequest) share.RunResult {
	return m.engine.Run(ctx, deps, req)
}
func (m *Supervisor) Commit(ctx context.Context, id string, update share.StateUpdate) (share.StateCommit, events.LogResult, error) {
	return m.state.Commit(ctx, id, update)
}
func (m *Supervisor) PrepareTools(ctx context.Context, names []string) (share.ToolPreload, error) {
	return m.preloader.PrepareTools(ctx, names)
}
func (m *Supervisor) PrepareSkills(ctx context.Context, prefix string, names []string) (share.SkillPreload, error) {
	return m.preloader.PrepareSkills(ctx, prefix, names)
}
func (m *Supervisor) PrepareBackground(ctx context.Context, req share.BackgroundPreloadRequest) (share.BackgroundPreload, error) {
	return m.preloader.PrepareBackground(ctx, req)
}
func (m *Supervisor) TagPrompts(ctx context.Context, tags []string) ([]share.TagPrompt, error) {
	return m.preloader.TagPrompts(ctx, tags)
}
func (m *Supervisor) List() []share.Info           { return m.catalog.Registry.List() }
func (m *Supervisor) Unregister(name string) error { return m.catalog.Registry.Unregister(name) }
func (m *Supervisor) Remove(ctx context.Context, name string) error {
	return m.catalog.SkillManager.Remove(ctx, name)
}
func (m *Supervisor) Reload(ctx context.Context) error { return m.catalog.SkillManager.Reload(ctx) }
func (m *Supervisor) Read(ctx context.Context, scope session.Scope) (resident.Memory, error) {
	return m.catalog.ResidentMemoryStore.Read(ctx, scope)
}
func (m *Supervisor) WithBinding(ctx context.Context, binding fileops.Binding, admission ...fileops.CommitAdmission) context.Context {
	return m.catalog.FileRollback.WithBinding(ctx, binding, admission...)
}
func (m *Supervisor) Forget(binding fileops.Binding) { m.catalog.FileRollback.Forget(binding) }
func (m *Supervisor) ListRollbacks(ctx context.Context) ([]fileops.RollbackInfo, error) {
	return m.catalog.FileRollback.List(ctx)
}
func (m *Supervisor) RollbackByID(ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	return m.catalog.FileRollback.RollbackByID(ctx, id)
}

func (m *Supervisor) ToolInfo(name string) (share.Info, bool) {
	return m.catalog.Registry.ToolInfo(name)
}
func (m *Supervisor) ToolSchema(name string) (llm.ToolSchema, bool) {
	return m.catalog.Registry.ToolSchema(name)
}
func (m *Supervisor) CallTool(ctx context.Context, req share.CallRequest) (*share.Result, string, error) {
	return m.engine.CallTool(ctx, req)
}
