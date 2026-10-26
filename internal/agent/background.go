package agent

import (
	"context"
	"time"

	agentinput "elbot/internal/agent/input"
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
func (m *Supervisor) RunBackground(ctx context.Context, req BackgroundRequest) (BackgroundResult, error) {
	ctx, done, err := m.backgroundContext(ctx)
	if err != nil {
		return BackgroundResult{}, err
	}
	defer done()
	result := m.background.RunBackground(ctx, agentinput.BackgroundRequest(req))
	return BackgroundResult{
		RunID: result.RunID, SessionID: result.SessionID, MessageID: result.MessageID,
		Text: result.Text, TakenOver: result.TakenOver, Outcome: result.Outcome,
	}, result.Err
}

// CopyBackground copies committed task history without activating a foreground binding.
func (m *Supervisor) CopyBackground(ctx context.Context, req BackgroundCopyRequest) (string, error) {
	ctx, done, err := m.backgroundContext(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	row, err := m.sessions.CopyBackground(ctx, req.Scope, session.BackgroundCopyRequest{
		SourceSessionID: req.SourceSessionID, Kind: req.Kind, Name: req.Name,
		Title: req.Title, Metadata: storage.SessionMetadata(req.Metadata),
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
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
