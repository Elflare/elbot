// 管理跨 attempt 的逻辑 Execution、接管、最终结果、完成通知与上下文绑定。
package turn

import (
	"context"
	"errors"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

// Execution spans individual requests and append-confirmation continuations.
// Its completion is independent of a canceled model request.
type Execution struct {
	ID                string
	mu                sync.Mutex
	foreground        context.Context
	foregroundSession storage.Session // Immutable committed handoff snapshot, paired with foreground under mu.
	result            Result
	done              chan struct{}
	finished          bool
}

func NewExecution(id string) *Execution {
	return &Execution{ID: id, done: make(chan struct{}), result: Result{RunID: id}}
}
func (e *Execution) Adopt(ctx context.Context, row storage.Session) {
	e.mu.Lock()
	e.foreground = context.WithoutCancel(ctx)
	e.foregroundSession = row
	e.result.TakenOver = true
	e.mu.Unlock()
}
func (e *Execution) Foreground() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.foreground
}

// ForegroundState reads routing and session facts from the same handoff.
func (e *Execution) ForegroundState() (context.Context, storage.Session) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.foreground, e.foregroundSession
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
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.finished {
		return
	}
	e.finished = true
	e.result.Err = err
	e.result.Outcome = "completed"
	if err != nil {
		e.result.Outcome = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			e.result.Outcome = "cancelled"
		}
	}
	close(e.done)
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

func WithExecution(ctx context.Context, e *Execution) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.RunID = ""
	if e != nil {
		facts.RunID = e.ID
	}
	ctx = contextinfo.WithExecution(ctx, facts)
	return context.WithValue(ctx, executionKey{}, e)
}
func ExecutionFromContext(ctx context.Context) *Execution {
	e, _ := ctx.Value(executionKey{}).(*Execution)
	return e
}
func WithAttempt(ctx context.Context, id string) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Attempt = id
	return contextinfo.WithExecution(ctx, facts)
}
func AttemptFromContext(ctx context.Context) string {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return facts.Attempt
}

// ResumeAppend keeps the logical execution but removes the old attempt before
// the caller admits a new request under the session admission gate.

// ExecutionAttempt reads both owners under the same lock for status validation.
