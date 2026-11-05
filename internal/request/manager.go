package request

import (
	"context"
	"sync"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

type Kind string

const (
	KindTurn     Kind = "turn"
	KindTool     Kind = "tool"
	KindHook     Kind = "hook"
	KindCompress Kind = "compress"
)

type Request struct {
	ID        string
	ParentID  string
	SessionID string
	Kind      Kind
	Label     string
	StartedAt time.Time
	// Stopping means cancellation was requested but execution has not acknowledged completion.
	Stopping bool
}

type StartRequest struct {
	ParentID  string
	SessionID string
	Kind      Kind
	Label     string
	Timeout   time.Duration
}

type Manager struct {
	next   storage.IDSequence // 根部注入的持久化序号能力。
	mu     sync.Mutex
	active map[string]*activeRequest
}

type activeRequest struct {
	request Request
	cancel  context.CancelFunc
	// ctx exposes inherited cancellation without an asynchronous cleanup watcher.
	ctx context.Context
}

func NewManager(next storage.IDSequence) *Manager {
	return &Manager{
		next:   next,
		active: map[string]*activeRequest{},
	}
}

// Start registers execution until its owner calls done, including after context
// cancellation. The owner must join its work and release resources before done.
func (m *Manager) Start(parent context.Context, start StartRequest) (Request, context.Context, func(), error) {
	id, err := m.next.ID(parent, "rq")
	if err != nil {
		return Request{}, nil, nil, err
	}
	startedAt := time.Now()
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if start.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, start.Timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}

	req := Request{
		ID:        id,
		ParentID:  start.ParentID,
		SessionID: start.SessionID,
		Kind:      start.Kind,
		Label:     start.Label,
		StartedAt: startedAt,
	}
	execution, _ := contextinfo.ExecutionFromContext(ctx)
	execution.RequestID, execution.ParentRequestID = req.ID, req.ParentID
	if req.SessionID != "" {
		execution.SessionID = req.SessionID
	}
	if req.Kind == KindTurn || execution.RootRequestID == "" {
		execution.RootRequestID = req.ID
	}
	ctx = contextinfo.WithExecution(ctx, execution)

	m.mu.Lock()
	m.active[req.ID] = &activeRequest{request: req, cancel: cancel, ctx: ctx}
	m.mu.Unlock()

	done := sync.OnceFunc(func() {
		m.mu.Lock()
		delete(m.active, req.ID)
		m.mu.Unlock()
		cancel()
	})

	return req, ctx, done, nil
}

func (m *Manager) List() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Request, 0, len(m.active))
	for _, active := range m.active {
		out = append(out, active.snapshot())
	}
	return out
}

func (m *Manager) ListBySession(sessionID string) []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Request{}
	for _, active := range m.active {
		if active.request.SessionID == sessionID {
			out = append(out, active.snapshot())
		}
	}
	return out
}

func (m *Manager) Get(id string) (Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	active, ok := m.active[id]
	if !ok {
		return Request{}, false
	}
	return active.snapshot(), true
}

func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	active, ok := m.active[id]
	ok = ok && !active.snapshot().Stopping
	if ok {
		active.request.Stopping = true
	}
	m.mu.Unlock()
	if ok {
		active.cancel()
	}
	return ok
}

func (m *Manager) CancelSession(sessionID string) int {
	m.mu.Lock()
	active := make([]*activeRequest, 0, len(m.active))
	for _, req := range m.active {
		if req.request.SessionID == sessionID && !req.snapshot().Stopping {
			req.request.Stopping = true
			active = append(active, req)
		}
	}
	m.mu.Unlock()
	for _, req := range active {
		req.cancel()
	}
	return len(active)
}

func (r *activeRequest) snapshot() Request {
	req := r.request
	req.Stopping = req.Stopping || r.ctx.Err() != nil
	return req
}
