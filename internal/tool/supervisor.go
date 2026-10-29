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
	toolstate "elbot/internal/tool/state"
)

// Supervisor 只持有内部组件并转接对外能力；装配和生命周期分别由 wiring.go、lifecycle.go 负责。
type Supervisor struct {
	catalog     *runtime
	engine      *execution.Manager
	preloader   *preload.PreloadService
	state       *toolstate.StateService
	ctx         context.Context                                // Tool 生命周期上下文。
	cancel      context.CancelFunc                             // 由生命周期入口取消后台工作。
	mu          sync.Mutex                                     // 保护启动、关闭状态。
	started     bool                                           // 标记已启动。
	closed      bool                                           // 标记已进入关闭阶段。
	done        chan struct{}                                  // 生命周期退出通知。
	cronChanged *signal.Signal[CronChangedEvent, signal.Empty] // 本实例任务定义变更通知。
}

func (m *Supervisor) QueryTools(ctx context.Context, query ToolQuery) ([]Info, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	infos, err := m.catalog.registry.QueryToolInfos(ctx, toInternalToolQuery(query))
	return toPublicInfos(infos), err
}

func (m *Supervisor) ToolTags(ctx context.Context) ([]string, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.preloader.Tags(ctx)
}

func (m *Supervisor) ToolNamesByTag(ctx context.Context, tag string, query ToolQuery) ([]string, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.preloader.ToolNamesByTag(ctx, tag, toInternalToolQuery(query))
}

func (m *Supervisor) DiscoveredToolsForSession(ctx context.Context, id string, background bool) ([]DiscoveredTool, events.LogResult, error) {
	items, result, err := m.state.DiscoveredToolsForSession(ctx, id, background)
	return toPublicDiscoveredTools(items), result, err
}

// DecodeState 解码调用方持有的会话快照，不另行读取持久化状态。
func (m *Supervisor) DecodeState(raw string) (State, error) {
	state, err := toolstate.DecodeState(raw)
	return toPublicState(state), err
}

func (m *Supervisor) Schemas(ctx context.Context, view Context, discovered []DiscoveredTool) ([]llm.ToolSchema, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.engine.Schemas(ctx, toInternalContext(view), toInternalDiscoveredTools(discovered))
}

// FilterSchemas 复核 Hook 修改后的模型声明，只筛除已注册且不适用于当前上下文的工具。
func (m *Supervisor) FilterSchemas(ctx context.Context, schemas []llm.ToolSchema) ([]llm.ToolSchema, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.engine.FilterSchemas(ctx, schemas)
}

func (m *Supervisor) Run(ctx context.Context, deps RunnerDeps, req RunRequest) RunResult {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return RunResult{Err: err}
	}
	return toPublicRunResult(m.engine.Run(ctx, executionBridge{RunnerDeps: deps}, toInternalRunRequest(req)))
}
func (m *Supervisor) Commit(ctx context.Context, id string, update StateUpdate) (StateCommit, events.LogResult, error) {
	committed, result, err := m.state.Commit(ctx, id, toInternalStateUpdate(update))
	return toPublicStateCommit(committed), result, err
}
func (m *Supervisor) PrepareTools(ctx context.Context, names []string) (ToolPreload, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return ToolPreload{}, err
	}
	prepared, err := m.preloader.PrepareTools(ctx, names)
	return toPublicToolPreload(prepared), err
}
func (m *Supervisor) PrepareSkills(ctx context.Context, prefix string, names []string) (SkillPreload, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return SkillPreload{}, err
	}
	prepared, err := m.preloader.PrepareSkills(ctx, prefix, names)
	return toPublicSkillPreload(prepared), err
}
func (m *Supervisor) PrepareBackground(ctx context.Context, req BackgroundPreloadRequest) (BackgroundPreload, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return BackgroundPreload{}, err
	}
	prepared, err := m.preloader.PrepareBackground(ctx, toInternalBackgroundPreloadRequest(req))
	return toPublicBackgroundPreload(prepared), err
}
func (m *Supervisor) BackgroundSelections(ctx context.Context, names []string) ([]BackgroundSelection, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	selections, err := m.preloader.BackgroundSelections(ctx, names)
	return toPublicBackgroundSelections(selections), err
}
func (m *Supervisor) TagPrompts(ctx context.Context, tags []string) ([]TagPrompt, error) {
	prompts, err := m.preloader.TagPrompts(ctx, tags)
	return toPublicTagPrompts(prompts), err
}
func (m *Supervisor) List(ctx context.Context) ([]Info, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return toPublicInfos(m.catalog.registry.List()), nil
}
func (m *Supervisor) Remove(ctx context.Context, name string) error {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return err
	}
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

func (m *Supervisor) ToolInfo(ctx context.Context, name string) (Info, bool, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return Info{}, false, err
	}
	info, ok := m.catalog.registry.ToolInfo(name)
	return toPublicInfo(info), ok, nil
}
func (m *Supervisor) ToolSchema(ctx context.Context, name string) (llm.ToolSchema, bool, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return llm.ToolSchema{}, false, err
	}
	schema, ok := m.catalog.registry.ToolSchema(name)
	return schema, ok, nil
}
func (m *Supervisor) CallTool(ctx context.Context, req CallRequest) (*Result, string, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, "", err
	}
	result, logContent, err := m.engine.CallTool(ctx, toInternalCallRequest(req))
	return toPublicResult(result), logContent, err
}
