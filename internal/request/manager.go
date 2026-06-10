package request

import (
	"context"
	"sync"
	"time"

	"elbot/internal/storage"
)

type Kind string

const (
	KindLLM      Kind = "llm"
	KindTool     Kind = "tool"
	KindCompress Kind = "compress"
	KindSubAgent Kind = "sub_agent"
)

type Request struct {
	ID        string
	SessionID string
	Kind      Kind
	Label     string
	StartedAt time.Time
	Deadline  *time.Time
}

type StartRequest struct {
	SessionID string
	Kind      Kind
	Label     string
	Timeout   time.Duration
}

type Manager struct {
	mu             sync.Mutex
	defaultTimeout time.Duration
	active         map[string]*activeRequest
}

type activeRequest struct {
	request Request
	cancel  context.CancelFunc
}

func NewManager(defaultTimeout time.Duration) *Manager {
	return &Manager{
		defaultTimeout: defaultTimeout,
		active:         map[string]*activeRequest{},
	}
}

func (m *Manager) Start(parent context.Context, start StartRequest) (Request, context.Context, func(), error) {
	if parent == nil {
		parent = context.Background()
	}

	timeout := start.Timeout
	if timeout == 0 {
		timeout = m.defaultTimeout
	}

	startedAt := time.Now()
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		deadline *time.Time
	)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
		d := startedAt.Add(timeout)
		deadline = &d
	} else {
		ctx, cancel = context.WithCancel(parent)
	}

	req := Request{
		ID:        storage.NewID(),
		SessionID: start.SessionID,
		Kind:      start.Kind,
		Label:     start.Label,
		StartedAt: startedAt,
		Deadline:  deadline,
	}
	if req.Kind == "" {
		req.Kind = KindLLM
	}

	m.mu.Lock()
	m.active[req.ID] = &activeRequest{request: req, cancel: cancel}
	m.mu.Unlock()

	done := sync.OnceFunc(func() {
		m.finish(req.ID, true)
	})
	go func() {
		<-ctx.Done()
		m.finish(req.ID, false)
	}()

	return req, ctx, done, nil
}

func (m *Manager) List() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Request, 0, len(m.active))
	for _, active := range m.active {
		out = append(out, active.request)
	}
	return out
}

func (m *Manager) ListBySession(sessionID string) []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Request{}
	for _, active := range m.active {
		if active.request.SessionID == sessionID {
			out = append(out, active.request)
		}
	}
	return out
}

func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	active, ok := m.active[id]
	if ok {
		delete(m.active, id)
	}
	m.mu.Unlock()
	if ok {
		active.cancel()
	}
	return ok
}

func (m *Manager) CancelSession(sessionID string) int {
	m.mu.Lock()
	active := []*activeRequest{}
	for id, req := range m.active {
		if req.request.SessionID == sessionID {
			delete(m.active, id)
			active = append(active, req)
		}
	}
	m.mu.Unlock()
	for _, req := range active {
		req.cancel()
	}
	return len(active)
}

func (m *Manager) CancelAll() int {
	m.mu.Lock()
	active := make([]*activeRequest, 0, len(m.active))
	for id, req := range m.active {
		delete(m.active, id)
		active = append(active, req)
	}
	m.mu.Unlock()
	for _, req := range active {
		req.cancel()
	}
	return len(active)
}

func (m *Manager) finish(id string, cancel bool) {
	m.mu.Lock()
	active, ok := m.active[id]
	if ok {
		delete(m.active, id)
	}
	m.mu.Unlock()
	if ok && cancel {
		active.cancel()
	}
}
