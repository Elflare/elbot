// 职责：构造 Session 总管，拆分配置并连接内部组件与外部依赖。
// 负责公开契约和内部契约之间的装配与转接。
package session

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/session/binding"
	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/session/naming"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// NewSupervisor 接收已加载的完整配置，构造阶段不启动任务或调用模型、材料能力。
// 出错时返回已取得的部分总管并停止任务接纳，Agent 仍须调用 Close 完成清理。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (s *Supervisor, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 必需依赖只在根部检查；配置默认值及有效性由配置层负责。
	switch {
	case cfg == nil:
		return nil, fmt.Errorf("session config is required")
	case deps.Store == nil:
		return nil, fmt.Errorf("session store is required")
	case deps.Models == nil:
		return nil, fmt.Errorf("session naming models are required")
	case deps.ActiveSessionIDs == nil:
		return nil, fmt.Errorf("session activity source is required")
	case deps.CheckForeground == nil:
		return nil, fmt.Errorf("session foreground check is required")
	case deps.Materials == nil:
		return nil, fmt.Errorf("session materials are required")
	case deps.IsSuperadmin == nil:
		return nil, fmt.Errorf("session permission query is required")
	case deps.ReleaseSession == nil:
		return nil, fmt.Errorf("session release is required")
	case deps.ReleaseBinding == nil:
		return nil, fmt.Errorf("binding release is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	s = &Supervisor{ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	defer func() {
		// 组件字段在回调可见前完成赋值，部分失败也登记同一停止流程。
		s.mu.Lock()
		s.stopParent = context.AfterFunc(ctx, s.BeginClose)
		s.mu.Unlock()
		if err != nil {
			s.BeginClose()
		}
	}()

	sessions, messages := deps.Store.Sessions(), deps.Store.Messages()
	s.bindings = binding.NewManager()
	// TODO(naming阶段)：将 naming.ForgetSession 与外部 ReleaseSession 组合后交给 control，命名缓存不交由 Agent 清理。
	s.control = control.NewService(
		control.Config{DefaultMode: cfg.Session.DefaultMode, IdleExpiration: control.IdleExpirationConfig(cfg.Session.IdleExpiration)},
		control.ServiceDeps{
			Sessions:         sessions,
			Messages:         messages,
			Bindings:         s.bindings,
			ActiveSessionIDs: deps.ActiveSessionIDs,
			CheckForeground:  deps.CheckForeground,
			IsSuperadmin:     func(scope share.Scope) bool { return deps.IsSuperadmin(Scope(scope)) },
			ReleaseSession:   deps.ReleaseSession,
			ReleaseBinding:   func(binding *share.Binding) { deps.ReleaseBinding((*Binding)(binding)) },
		},
	)
	materials := deps.Materials
	s.branches = branch.NewService(branch.ServiceDeps{
		Sessions:         sessions,
		Messages:         messages,
		Dialogues:        deps.Store.Dialogues(),
		Bindings:         s.control,
		ActiveSessionIDs: deps.ActiveSessionIDs,
		MaterialFor: func(origin llm.Origin) (branch.MaterialPreparer, error) {
			preparer, err := materials.MaterialFor(origin)
			if err != nil {
				return nil, err
			}
			return materialPreparer{source: preparer}, nil
		},
	})
	s.naming, err = naming.NewService(
		naming.Config{TriggerStep: cfg.Session.Naming.TriggerStep},
		naming.ServiceDeps{
			Sessions:      sessions,
			Messages:      messages,
			ResolveNaming: deps.Models.ResolveNaming,
		},
	)
	if err != nil {
		return s, fmt.Errorf("session naming: %w", err)
	}
	// 接纳构造期间发生的取消；已创建的组件仍交回上层关闭。
	return s, ctx.Err()
}

// materialPreparer 将提供方根部契约投影为 branch 独用的材料契约。
type materialPreparer struct {
	source MaterialPreparer
}

func (p materialPreparer) PrepareFork(ctx context.Context, row *storage.Session, message *storage.Message) (*branch.PreparedMaterial, error) {
	prepared, err := p.source.PrepareFork(ctx, row, message)
	return internalMaterial(prepared), err
}

func (p materialPreparer) PrepareCopy(ctx context.Context, row *storage.Session) (*branch.PreparedMaterial, error) {
	prepared, err := p.source.PrepareCopy(ctx, row)
	return internalMaterial(prepared), err
}

// internalMaterial 保留无原生材料的 nil 语义，不丢弃伴随错误返回的部分结果。
func internalMaterial(prepared *PreparedMaterial) *branch.PreparedMaterial {
	if prepared == nil {
		return nil
	}
	return &branch.PreparedMaterial{
		Seed:                 prepared.Seed,
		ExpectedCheckpointID: prepared.ExpectedCheckpointID,
	}
}
