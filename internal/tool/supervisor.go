// 承接 Tool 对外能力入口，转接内部服务。
package tool

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"fmt"
	"slices"
	"sync"

	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/share"
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

// RegisterTools 完整提交一个来源的定义；远端返回会话事务待提交的补丁。
func (m *Supervisor) RegisterTools(ctx context.Context, source Source, scope ToolRegistrationScope, definitions []ToolDefinition) (StateUpdate, error) {
	internal := make([]share.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		value := share.ToolDefinition{
			Definition:       definition.Definition,
			Parameters:       definition.Parameters,
			ActivateTools:    definition.ActivateTools,
			DiscoveryContent: definition.DiscoveryContent,
		}
		if definition.Remote != nil {
			remote := share.ELwispToolDeclaration(*definition.Remote)
			value.Remote = &remote
		}
		if definition.LoadDetail != nil {
			value.LoadDetail = func(ctx context.Context) (share.DetailBlock, error) {
				block, err := definition.LoadDetail(ctx)
				return share.DetailBlock(block), err
			}
		}
		if definition.Prepare != nil {
			value.Prepare = func(ctx context.Context, req share.CallRequest) (operationresult.Result[*share.PreparedCall], error) {
				prepared, err := definition.Prepare(ctx, CallRequest(req))
				if err != nil {
					return operationresult.Result[*share.PreparedCall]{}, err
				}
				if prepared.Status != operationresult.Success {
					return operationresult.Result[*share.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
				}
				if prepared.Data == nil || prepared.Data.Run == nil {
					return operationresult.Result[*share.PreparedCall]{}, fmt.Errorf("tool %q returned no prepared operation", req.Name)
				}
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: share.NewPreparedCall(req, share.RiskAssessment(prepared.Data.Risk), prepared.Data.Details, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
					result, logContent, err := prepared.Data.Run(ctx)
					if err != nil {
						return operationresult.Result[*share.Result]{}, logContent, err
					}
					if result.Status != operationresult.Success {
						return operationresult.Result[*share.Result]{Status: result.Status, Message: result.Message}, logContent, nil
					}
					return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: toInternalResult(result.Data)}, logContent, nil
				})}, nil
			}
		}
		internal = append(internal, value)
	}
	update, err := m.catalog.registry.RegisterTools(ctx, source, share.ToolRegistrationScope(scope), internal)
	return toPublicStateUpdate(update), err
}

// QueryTools 返回只读工具描述；修改 Parameters 或 Definition 中的切片前须自行复制。
func (m *Supervisor) QueryTools(ctx context.Context, query ToolQuery) ([]ToolInfo, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	definitions, err := m.catalog.registry.Query(ctx, toInternalToolQuery(query))
	if err != nil {
		return nil, err
	}
	if query.Tag != "" {
		names, err := m.preloader.NamesByTag(ctx, query.Tag, definitions)
		if err != nil {
			return nil, err
		}
		definitions = slices.DeleteFunc(definitions, func(definition share.ToolDefinition) bool {
			return !slices.Contains(names, definition.Definition.Name)
		})
	}
	infos := make([]ToolInfo, len(definitions))
	for i, definition := range definitions {
		infos[i] = ToolInfo{Definition: definition.Definition, Parameters: definition.Parameters}
	}
	return infos, nil
}

func (m *Supervisor) LoadToolDetail(ctx context.Context, name string) (operationresult.Result[DetailBlock], error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return operationresult.Result[DetailBlock]{}, err
	}
	definitions, err := m.catalog.registry.Query(ctx, share.ToolQuery{
		Scope: share.QueryCatalog, Names: []string{name}, IncludeHidden: true, IncludeDiscover: true,
	})
	if err != nil {
		return operationresult.Result[DetailBlock]{}, err
	}
	if len(definitions) == 0 || definitions[0].LoadDetail == nil {
		return operationresult.Result[DetailBlock]{Status: operationresult.NotFound, Message: fmt.Sprintf("tool %q detail not found", name)}, nil
	}
	block, err := definitions[0].LoadDetail(ctx)
	if err != nil {
		return operationresult.Result[DetailBlock]{}, err
	}
	return operationresult.Result[DetailBlock]{Status: operationresult.Success, Data: DetailBlock(block)}, nil
}

func (m *Supervisor) ToolTags(ctx context.Context) ([]string, error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.preloader.Tags(ctx)
}

func (m *Supervisor) DiscoveredToolsForSession(ctx context.Context, id string, background bool) ([]DiscoveredTool, error) {
	items, err := m.state.DiscoveredToolsForSession(ctx, id, background)
	return toPublicDiscoveredTools(items), err
}

// DecodeState 解码调用方持有的会话快照，不另行读取持久化状态。
func (m *Supervisor) DecodeState(raw string) (State, error) {
	state, err := toolstate.DecodeState(raw)
	return toPublicState(state), err
}

func (m *Supervisor) Schemas(ctx context.Context, view Context, discovered []DiscoveredTool) ([]llm.ToolSchema, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.engine.Schemas(ctx, toInternalContext(view), toInternalDiscoveredTools(discovered))
}

// FilterSchemas 复核 Hook 修改后的模型声明，只筛除已注册且不适用于当前上下文的工具。
func (m *Supervisor) FilterSchemas(ctx context.Context, schemas []llm.ToolSchema) ([]llm.ToolSchema, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.engine.FilterSchemas(ctx, schemas)
}

func (m *Supervisor) Run(ctx context.Context, deps RunnerDeps, req RunRequest) RunResult {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return RunResult{Err: err}
	}
	return toPublicRunResult(m.engine.Run(ctx, executionBridge{RunnerDeps: deps}, toInternalRunRequest(req)))
}
func (m *Supervisor) Commit(ctx context.Context, id string, update StateUpdate) (StateCommit, error) {
	committed, err := m.state.Commit(ctx, id, toInternalStateUpdate(update))
	return toPublicStateCommit(committed), err
}
func (m *Supervisor) PrepareTools(ctx context.Context, names []string) (ToolPreload, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return ToolPreload{}, err
	}
	prepared, err := m.preloader.PrepareTools(ctx, names)
	return toPublicToolPreload(prepared), err
}
func (m *Supervisor) PrepareSkills(ctx context.Context, prefix string, names []string) (SkillPreload, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return SkillPreload{}, err
	}
	prepared, err := m.preloader.PrepareSkills(ctx, prefix, names)
	return toPublicSkillPreload(prepared), err
}
func (m *Supervisor) PrepareBackground(ctx context.Context, req BackgroundPreloadRequest) (BackgroundPreload, error) {
	ctx = toolPolicyContext(ctx)
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
func (m *Supervisor) Remove(ctx context.Context, name string) (operationresult.Result[struct{}], error) {
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return operationresult.Result[struct{}]{}, err
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
func (m *Supervisor) ListRollbacks(ctx context.Context) (operationresult.Result[[]fileops.RollbackInfo], error) {
	return m.catalog.fileRollback.List(ctx)
}
func (m *Supervisor) RollbackByID(ctx context.Context, id uint64) (operationresult.Result[fileops.RollbackResult], error) {
	return m.catalog.fileRollback.RollbackByID(ctx, id)
}

func (m *Supervisor) CallTool(ctx context.Context, req CallRequest) (operationresult.Result[*Result], string, error) {
	ctx = toolPolicyContext(ctx)
	if err := m.catalog.skillManager.EnsureLoaded(ctx); err != nil {
		return operationresult.Result[*Result]{}, "Tool execution not entered: " + err.Error(), err
	}
	result, logContent, err := m.engine.CallTool(ctx, toInternalCallRequest(req))
	if err != nil {
		return operationresult.Result[*Result]{}, logContent, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[*Result]{Status: result.Status, Message: result.Message}, logContent, nil
	}
	return operationresult.Result[*Result]{Status: operationresult.Success, Data: toPublicResult(result.Data)}, logContent, nil
}

// toolPolicyContext 在 Tool 边界解释执行事实，内部只接收所需策略。
func toolPolicyContext(ctx context.Context) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	_, hasChat := contextinfo.ConversationFromContext(ctx)
	policy := share.CallPolicy{
		DisableForegroundTools: facts.Mode == "background",
		ExplicitChat:           !hasChat,
		AutoConfirmShell:       facts.Mode == "background" && (facts.TaskType == "cron" || facts.TaskType == "elnis"),
	}
	if facts.Mode == "background" && facts.TaskType == "cron" {
		actor, _ := contextinfo.ActorFromContext(ctx)
		policy.OutputTarget = delivery.Target{Platform: actor.Platform, Superadmins: true}
	}
	return share.WithCallPolicy(ctx, policy)
}
