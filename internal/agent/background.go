package agent

import (
	"context"
	"time"

	agentinput "elbot/internal/agent/input"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// backgroundContext preserves caller values and cancellation while also observing Agent shutdown.
// The caller joins the synchronous operation before closing Agent's owned resources.
func (m *Supervisor) backgroundContext(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	if m.ctx.Err() != nil {
		cancel()
	}
	done := func() {
		stop()
		cancel()
	}
	if err := ctx.Err(); err != nil {
		done()
		return nil, nil, err
	}
	return ctx, done, nil
}

// RunBackground prepares and executes a task without activating a foreground session.
// The caller supplies its prepared workspace scope and background source in ctx.
func (m *Supervisor) RunBackground(ctx context.Context, req BackgroundRequest) (context.Context, operationresult.Result[BackgroundResult], error) {
	prepared, done, err := m.backgroundContext(ctx)
	if err != nil {
		return ctx, operationresult.Result[BackgroundResult]{}, err
	}
	defer done()
	prepared = m.restoreMessageContext(prepared, context.Background())
	prepared = agentshare.WithMessageContext(prepared, agentshare.MessageContext{RestoreMessageContext: m.restoreMessageContext})
	prepared, result, err := m.background.RunBackground(prepared, agentinput.BackgroundRequest(req))
	// Export task facts without exporting this call's canceled child lifetime.
	ctx = m.restoreMessageContext(ctx, prepared)
	facts, _ := contextinfo.ExecutionFromContext(prepared)
	ctx = contextinfo.WithExecution(ctx, facts)
	if model, ok := contextinfo.ModelFromContext(prepared); ok {
		ctx = contextinfo.WithModel(ctx, model)
	}
	if actor, ok := contextinfo.ActorFromContext(prepared); ok {
		ctx = contextinfo.WithActor(ctx, actor)
	}
	ctx = contextinfo.WithoutConversation(ctx)
	if conversation, ok := contextinfo.ConversationFromContext(prepared); ok {
		ctx = contextinfo.WithConversation(ctx, conversation)
	}
	if err != nil {
		return ctx, operationresult.Result[BackgroundResult]{}, err
	}
	if result.Status != operationresult.Success {
		return ctx, operationresult.Result[BackgroundResult]{Status: result.Status, Message: result.Message}, nil
	}
	data := result.Data
	return ctx, operationresult.Result[BackgroundResult]{Status: operationresult.Success, Data: BackgroundResult{
		RunID: data.RunID, SessionID: data.SessionID, MessageID: data.MessageID,
		Text: data.Text, TakenOver: data.TakenOver, Outcome: data.Outcome,
	}}, nil
}

// CopyBackground copies committed task history without activating a foreground binding.
func (m *Supervisor) CopyBackground(ctx context.Context, req BackgroundCopyRequest) (operationresult.Result[string], error) {
	ctx, done, err := m.backgroundContext(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	defer done()
	result, err := m.sessions.CopyBackground(ctx, req.Scope, session.BackgroundCopyRequest{
		SourceSessionID: req.SourceSessionID, TaskType: req.TaskType, TaskID: req.TaskID,
		Name: req.Name, Metadata: storage.SessionMetadata(req.Metadata),
	})
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[string]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[string]{Status: result.Status, Message: result.Message, Data: result.Data.ID}, nil
}

// CleanupSessions removes expired idle sessions and returns the count completed before any error.
func (m *Supervisor) CleanupSessions(ctx context.Context, cutoff time.Time) (int, error) {
	ctx, done, err := m.backgroundContext(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	return m.sessions.CleanupExpired(ctx, cutoff)
}

// SessionTakenOver 查询后台会话是否已被前台接管。
func (m *Supervisor) SessionTakenOver(ctx context.Context, id string) (bool, error) {
	ctx, done, err := m.backgroundContext(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	return m.sessions.SessionTakenOver(ctx, id)
}
