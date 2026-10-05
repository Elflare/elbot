package turn

import (
	"context"
	"errors"
	"sync"
)

// Execution spans individual requests and append-confirmation continuations.
// Its completion is independent of a canceled model request.
type Execution struct {
	ID         string
	mu         sync.Mutex
	foreground context.Context
	result     Result
	done       chan struct{}
	once       sync.Once
	finished   bool
}

type Result struct {
	RunID     string
	SessionID string
	MessageID string
	Text      string
	TakenOver bool
	Outcome   string
	Err       error
}

func NewExecution(id string) *Execution {
	return &Execution{ID: id, done: make(chan struct{}), result: Result{RunID: id}}
}
func (e *Execution) Adopt(ctx context.Context) {
	e.mu.Lock()
	e.foreground = context.WithoutCancel(ctx)
	e.result.TakenOver = true
	e.mu.Unlock()
}
func (e *Execution) Foreground() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.foreground
}
func (e *Execution) SetResult(sessionID, messageID, text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.finished {
		return
	}
	e.result.SessionID, e.result.MessageID, e.result.Text = sessionID, messageID, text
}
func (e *Execution) Finish(err error) {
	e.once.Do(func() {
		e.mu.Lock()
		e.finished = true
		e.result.Err = err
		e.result.Outcome = "completed"
		if err != nil {
			e.result.Outcome = "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				e.result.Outcome = "cancelled"
			}
		}
		e.mu.Unlock()
		close(e.done)
	})
}
func (e *Execution) Wait(ctx context.Context) Result {
	select {
	case <-e.done:
	case <-ctx.Done():
		e.Finish(ctx.Err())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}
func (e *Execution) Done() <-chan struct{} { return e.done }

type executionKey struct{}
type attemptKey struct{}

func WithExecution(ctx context.Context, e *Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, e)
}
func ExecutionFromContext(ctx context.Context) *Execution {
	e, _ := ctx.Value(executionKey{}).(*Execution)
	return e
}
func WithAttempt(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, attemptKey{}, id)
}
func AttemptFromContext(ctx context.Context) string {
	id, _ := ctx.Value(attemptKey{}).(string)
	return id
}

func matches(turn *state, expected []string) bool {
	return turn != nil && (len(expected) == 0 || turn.compactRun == expected[0])
}

func (m *Manager) Execution(sessionID string) *Execution {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.turns[sessionID]; s != nil {
		return s.execution
	}
	return nil
}

// ResumeAppend keeps the logical execution but removes the old attempt before
// the caller admits a new request under the session admission gate.
func (m *Manager) ResumeAppend(sessionID string) (Input, *Execution, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.turns[sessionID]
	if s == nil || s.phase != PhaseAwaitAppendConfirm {
		return Input{}, nil, false
	}
	input := mergeInputs(append([]Input{s.originalInput}, s.pending...))
	e := s.execution
	if s.appendDone != nil {
		close(s.appendDone)
		s.appendDone = nil
	}
	s.phase = PhaseLLM
	s.originalInput = input
	s.pending = nil
	s.compactRun = ""
	s.reserved = true
	return input, e, true
}

func (m *Manager) StartExecution(sessionID string, input Input, e *Execution, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.turns[sessionID]; old != nil && !(old.reserved && old.execution == e && old.phase == PhaseLLM) {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, compactRun: attempt}
	return true
}

func (m *Manager) AttachExecution(sessionID, attempt string, e *Execution) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.turns[sessionID]; matches(s, []string{attempt}) {
		s.execution = e
	}
}

func (m *Manager) MatchesAttempt(sessionID, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return matches(m.turns[sessionID], []string{attempt})
}

// ExecutionAttempt reads both owners under the same lock for status validation.
func (m *Manager) ExecutionAttempt(sessionID string) (*Execution, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.turns[sessionID]
	if s == nil {
		return nil, "", false
	}
	return s.execution, s.compactRun, true
}

func (m *Manager) ReserveExecution(sessionID string, input Input, e *Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turns[sessionID] != nil {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, reserved: true}
	return true
}

func (m *Manager) CanCompact(sessionID string, e *Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.turns[sessionID]
	return s == nil || (s.reserved && s.compactReady && s.phase == PhaseLLM && s.execution == e)
}
