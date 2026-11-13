// 职责：构造 Session 总管，拆分配置并连接内部组件与外部依赖。
// 负责公开契约和内部契约之间的装配与转接。
package session

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"

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
	s.nextSequence = sessions.NextSequence
	s.sessionIDCode = deps.SessionIDCode
	s.bindings = binding.NewManager()
	s.control = control.NewService(
		control.Config{DefaultMode: cfg.Session.DefaultMode, IdleExpiration: control.IdleExpirationConfig(cfg.Session.IdleExpiration)},
		control.ServiceDeps{
			AdoptForeground: func(ctx context.Context, row *storage.Session, b *share.Binding) {
				deps.AdoptForeground(ctx, row, (*Binding)(b))
			},
			AllocateID:       s.allocateID,
			Sessions:         sessions,
			Messages:         messages,
			Bindings:         s.bindings,
			ActiveSessionIDs: deps.ActiveSessionIDs,
			CheckForeground:  deps.CheckForeground,
			IsSuperadmin:     func(scope share.Scope) bool { return deps.IsSuperadmin(Scope(scope)) },
			ReleaseSession: func(id string) {
				s.naming.ForgetSession(id)
				deps.ReleaseSession(id)
			},
			ReleaseBinding: func(binding *share.Binding) { deps.ReleaseBinding((*Binding)(binding)) },
		},
	)
	materials := deps.Materials
	s.branches = branch.NewService(branch.ServiceDeps{
		NewMessageID:     storage.IDSequence(deps.Store.NextIDSequence).MessageID,
		AllocateID:       s.allocateID,
		Sessions:         sessions,
		Messages:         messages,
		ToolCalls:        deps.Store.ToolCalls(),
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
	s.naming = naming.NewService(
		naming.Config{TriggerStep: cfg.Session.Naming.TriggerStep},
		naming.ServiceDeps{
			Log: func(ctx context.Context, record events.LogRecord) {
				events.Log(context.WithoutCancel(ctx), record)
			},
			Sessions:      sessions,
			Messages:      messages,
			ResolveNaming: deps.Models.ResolveNaming,
		},
	)
	// 接纳构造期间发生的取消；已创建的组件仍交回上层关闭。
	return s, ctx.Err()
}

// materialPreparer 将提供方根部契约投影为 branch 独用的材料契约。
type materialPreparer struct {
	source MaterialPreparer
}

func (p materialPreparer) PrepareFork(ctx context.Context, row *storage.Session, message *storage.Message) (operationresult.Result[*branch.PreparedMaterial], error) {
	prepared, err := p.source.PrepareFork(ctx, row, message)
	if err != nil {
		return operationresult.Result[*branch.PreparedMaterial]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*branch.PreparedMaterial]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	return operationresult.Result[*branch.PreparedMaterial]{Status: operationresult.Success, Data: internalMaterial(prepared.Data)}, nil
}

func (p materialPreparer) PrepareCopy(ctx context.Context, row *storage.Session) (operationresult.Result[*branch.PreparedMaterial], error) {
	prepared, err := p.source.PrepareCopy(ctx, row)
	if err != nil {
		return operationresult.Result[*branch.PreparedMaterial]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*branch.PreparedMaterial]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	return operationresult.Result[*branch.PreparedMaterial]{Status: operationresult.Success, Data: internalMaterial(prepared.Data)}, nil
}

// internalMaterial 保留成功时无原生材料的空数据。
func internalMaterial(prepared *PreparedMaterial) *branch.PreparedMaterial {
	if prepared == nil {
		return nil
	}
	return &branch.PreparedMaterial{
		Seed:                 prepared.Seed,
		ExpectedCheckpointID: prepared.ExpectedCheckpointID,
	}
}
