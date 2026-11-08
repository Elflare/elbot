// 提供 Hook 总管及面向 App 的公开管理入口。

package hook

import (
	"context"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

// Supervisor owns Hook components and boundary connections, not Agent state or
// the borrowed Tool, Platform and media services. Component handles stay private.
type Supervisor struct {
	manager interface {
		Run(context.Context, share.Event, pipeline.CallOptions) (share.Event, error)
		Notify(context.Context, share.Event, pipeline.CallOptions) error
	}
	runtime interface {
		HasLease(share.Event) bool
		Cancel(share.Event) bool
		Close(context.Context) error
		Done() <-chan struct{}
	}
	control interface {
		HookReload() (share.ReloadReport, error)
		HookList() []share.Info
		StatefulHooks() []share.RuntimeInfo
		StartStatefulHook(string) error
		StopHook(context.Context, string) (bool, error)
		RestartStatefulHook(context.Context, string) error
	}
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)

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

// run admits an external call and supplies its facts; pipeline owns execution.
func (m *Supervisor) run(ctx context.Context, event share.Event, options pipeline.CallOptions) (share.Event, error) {
	ctx, done, err := m.beginCall(ctx)
	if err != nil {
		return event, err
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
	_ = m.manager.Notify(ctx, fillEventFromContext(ctx, event), options)
}
