// 提供 Hook 总管及面向 App 的公开管理入口。

package hook

import (
	"context"
	"sync"

	"elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

// Supervisor owns Hook components and boundary connections, not Agent state or
// the borrowed Tool, Platform and media services. Component handles stay private.
type Supervisor struct {
	manager interface {
		Run(context.Context, share.Event, pipeline.CallOptions) (share.Event, events.LogResult, error)
		Notify(context.Context, share.Event, pipeline.CallOptions) (events.LogResult, error)
	}
	runtime interface {
		RouteHookID(share.Event) string
		Cancel(share.Event) bool
		Close(context.Context) error
		Done() <-chan struct{}
	}
	control interface {
		HookReload() (share.ReloadReport, error)
	}
	commands []command.Handler // 已装配的管理命令能力；外部仅取得标准 Handler。
	send     func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)

	parent         context.Context // Construction context, used for startup and platform subscription ownership.
	ctx            context.Context // Hook-owned cancellation for admitted boundary calls; canceled only by Close.
	cancel         context.CancelFunc
	mu             sync.Mutex
	startMu        sync.Mutex // Serializes Start until loading and platform subscription are complete.
	started        bool
	closed         bool           // BeginClose rejects new starts/connections while retaining existing Agent callbacks.
	closing        bool           // Close rejects all new calls before waiting for the admitted calls.
	calls          sync.WaitGroup // Boundary invocations only; handler and waiting state remain in the children.
	disconnect     func()         // Owned Agent registration; no Agent supervisor is retained here.
	platformEvents platformSubscriptions
	closeOnce      sync.Once
	closeErr       error         // Actual shutdown result, published before done closes.
	done           chan struct{} // All boundary calls, platform callbacks and runtime resources have exited.
}

// Commands returns a detached collection of callable management commands.
func (m *Supervisor) Commands() []command.Handler {
	return append([]command.Handler(nil), m.commands...)
}

// run admits an external call and supplies its facts; pipeline owns execution.
func (m *Supervisor) run(ctx context.Context, event share.Event, options pipeline.CallOptions) (share.Event, events.LogResult, error) {
	ctx, done, err := m.beginCall(ctx)
	if err != nil {
		return event, events.LogResult{ResultStatus: events.ResultCanceled}, err
	}
	defer done()
	return m.manager.Run(ctx, fillEventFromContext(ctx, event), options)
}

func (m *Supervisor) notify(ctx context.Context, event share.Event, options pipeline.CallOptions) {
	ctx, done, err := m.beginCall(ctx)
	if err != nil {
		return
	}
	defer done()
	_, _ = m.manager.Notify(ctx, fillEventFromContext(ctx, event), options)
}
