// 职责：定义分支组件、所需依赖及材料准备流程。
// 通过注入的协议材料能力准备快照，并组装原子创建所需数据。
package branch

import (
	"context"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

type ServiceDeps struct {
	Sessions interface {
		Get(context.Context, string) (*storage.Session, error)
		Create(context.Context, *storage.Session) error
		CreateMaterial(context.Context, storage.SessionMaterialCreate) (events.LogResult, error)
	}
	Messages interface {
		Get(context.Context, string) (*storage.Message, error)
		ListBySession(context.Context, string) ([]storage.Message, error)
	}
	Dialogues interface {
		PendingInputs(context.Context, string) ([]storage.NativeInput, error)
	}
	Bindings         share.BindingCoordinator                   // 根部注入 Control，派生激活经过统一计时及退役。
	ActiveSessionIDs func() []string                            // 执行所有者提供活跃事实，包含实际收尾阶段。
	MaterialFor      func(llm.Origin) (MaterialPreparer, error) // 按保存的协议归属取得材料能力。
}

type MaterialPreparer interface {
	PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error)
	PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error)
}

type PreparedMaterial struct {
	Seed                 *storage.NativeSeed // 协议组件准备的独立原生根；Branch 不解释其 JSON。
	ExpectedCheckpointID string              // 对应源游标，由存储在创建事务内复核。
}

// Service 无自有任务或资源，依赖由根部检查并借用。
type Service struct{ deps ServiceDeps }

func NewService(deps ServiceDeps) *Service { return &Service{deps: deps} }

// prepareMaterial 只在业务准入外调用；没有保存协议归属时不推断路线。
func (s *Service) prepareMaterial(ctx context.Context, origin llm.Origin, source *storage.Session, message *storage.Message) (*PreparedMaterial, error) {
	if origin.APIType == "" {
		return nil, nil
	}
	preparer, err := s.deps.MaterialFor(origin)
	if err != nil {
		return nil, err
	}
	if message != nil {
		return preparer.PrepareFork(ctx, source, message)
	}
	return preparer.PrepareCopy(ctx, source)
}

func materialCreate(row *storage.Session, sourceID string, prepared *PreparedMaterial, messages []*storage.Message) storage.SessionMaterialCreate {
	req := storage.SessionMaterialCreate{Session: row, Messages: messages}
	if prepared != nil {
		req.Seed, req.SourceSessionID, req.ExpectedCheckpointID = prepared.Seed, sourceID, prepared.ExpectedCheckpointID
	}
	return req
}

func (s *Service) requireIdle(id string) error {
	for _, active := range s.deps.ActiveSessionIDs() {
		if active == id {
			return share.ErrSessionBusy
		}
	}
	return nil
}
