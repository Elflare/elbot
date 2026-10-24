// 提供 Hook 总管及面向 App 的公开管理入口。

package hook

import (
	"context"
	"sync"

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
		HookList() []share.Info
		HookReload() (share.ReloadReport, error)
		StatefulHooks() []share.RuntimeInfo
		StartStatefulHook(string) error
		StopHook(context.Context, string) (bool, error)
		RestartStatefulHook(context.Context, string) error
	}
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)

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

func (m *Supervisor) HookList() []share.Info {
	_, done, err := m.beginCall(context.Background())
	if err != nil {
		return nil
	}
	defer done()
	return m.control.HookList()
}

func (m *Supervisor) HookReload() (share.ReloadReport, error) {
	_, done, err := m.beginCall(context.Background())
	if err != nil {
		return share.ReloadReport{}, err
	}
	defer done()
	return m.control.HookReload()
}

func (m *Supervisor) StatefulHooks() []share.RuntimeInfo {
	_, done, err := m.beginCall(context.Background())
	if err != nil {
		return nil
	}
	defer done()
	return m.control.StatefulHooks()
}

func (m *Supervisor) StartStatefulHook(id string) error {
	_, done, err := m.beginCall(context.Background())
	if err != nil {
		return err
	}
	defer done()
	return m.control.StartStatefulHook(id)
}

func (m *Supervisor) StopHook(ctx context.Context, id string) (bool, error) {
	ctx, done, err := m.beginCall(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	return m.control.StopHook(ctx, id)
}

func (m *Supervisor) RestartStatefulHook(ctx context.Context, id string) error {
	ctx, done, err := m.beginCall(ctx)
	if err != nil {
		return err
	}
	defer done()
	return m.control.RestartStatefulHook(ctx, id)
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
