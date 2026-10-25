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
	"elbot/internal/signal"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/share"
	toolstate "elbot/internal/tool/state"
)

// Supervisor 只持有内部组件并转接对外能力；装配和生命周期分别由 wiring.go、lifecycle.go 负责。
type Supervisor struct {
	catalog                *runtime
	engine                 *execution.Manager
	preloader              *preload.PreloadService
	state                  *toolstate.StateService
	ctx                    context.Context    // Tool 生命周期上下文。
	cancel                 context.CancelFunc // 由生命周期入口取消后台工作。
	mu                     sync.Mutex         // 保护启动、关闭状态。
	started                bool               // 标记已启动。
	closed                 bool               // 标记已进入关闭阶段。
	done                   chan struct{}      // 生命周期退出通知。
	releaseCronTools func() // 释放本总管持有的 Cron 工具注册。
	cronChanged *signal.Signal[CronChangedEvent, signal.Empty] // 本实例任务定义变更通知。
}

func (m *Supervisor) QueryTools(ctx context.Context, query share.ToolQuery) ([]share.Info, error) {
	return m.catalog.registry.QueryToolInfos(ctx, query)
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

// DecodeState 解码调用方持有的会话快照，不另行读取持久化状态。
func (m *Supervisor) DecodeState(raw string) (share.State, error) {
	return toolstate.DecodeState(raw)
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
// BackgroundSelection 是授权前展开的工具名单，公开类型不依赖内部 share。
type BackgroundSelection struct {
	Names []string // 选择器展开得到的根工具名称。
	Tag string // 使用标签选择时的标签名称。
}

func (m *Supervisor) BackgroundSelections(ctx context.Context, names []string) ([]BackgroundSelection, error) {
	selections, err := m.preloader.BackgroundSelections(ctx, names)
	if err != nil {
		return nil, err
	}
	result := make([]BackgroundSelection, len(selections))
	for i, item := range selections {
		result[i] = BackgroundSelection{Names: append([]string(nil), item.Names...), Tag: item.Tag}
	}
	return result, nil
}
func (m *Supervisor) TagPrompts(ctx context.Context, tags []string) ([]share.TagPrompt, error) {
	return m.preloader.TagPrompts(ctx, tags)
}
func (m *Supervisor) List() []share.Info           { return m.catalog.registry.List() }
func (m *Supervisor) Unregister(name string) error { return m.catalog.registry.Unregister(name) }
func (m *Supervisor) Remove(ctx context.Context, name string) error {
	return m.catalog.skillManager.Remove(ctx, name)
}
func (m *Supervisor) Reload(ctx context.Context) error { return m.catalog.skillManager.Reload(ctx) }
func (m *Supervisor) Read(ctx context.Context, scope session.Scope) (resident.Memory, error) {
	return m.catalog.residentMemoryStore.Read(ctx, scope)
}
func (m *Supervisor) WithBinding(ctx context.Context, binding fileops.Binding, admission ...fileops.CommitAdmission) context.Context {
	return m.catalog.fileRollback.WithBinding(ctx, binding, admission...)
}
func (m *Supervisor) Forget(binding fileops.Binding) { m.catalog.fileRollback.Manager.Forget(binding) }
func (m *Supervisor) ListRollbacks(ctx context.Context) ([]fileops.RollbackInfo, error) {
	return m.catalog.fileRollback.List(ctx)
}
func (m *Supervisor) RollbackByID(ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	return m.catalog.fileRollback.RollbackByID(ctx, id)
}

func (m *Supervisor) ToolInfo(name string) (share.Info, bool) {
	return m.catalog.registry.ToolInfo(name)
}
func (m *Supervisor) ToolSchema(name string) (llm.ToolSchema, bool) {
	return m.catalog.registry.ToolSchema(name)
}
func (m *Supervisor) CallTool(ctx context.Context, req share.CallRequest) (*share.Result, string, error) {
	return m.engine.CallTool(ctx, req)
}
