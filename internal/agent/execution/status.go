package execution

import (
	"context"
	"sync"
	"time"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// StatusRecorder is the synchronous source of runtime snapshots. Display
// delivery happens after Record returns and never while its mutex is held.
type StatusRecorder struct {
	publishMu sync.Mutex
	mu        sync.Mutex
	snapshots map[string]runtimestatus.Snapshot
	owners    map[string]contextinfo.Execution
	version   uint64
	turns     *turn.Manager
	changed   *signal.Signal[agentevents.StatusChangedEvent, signal.Empty]
	view      dialogue.ExecutionView
}

func (r *StatusRecorder) Record(ctx context.Context, snapshot runtimestatus.Snapshot) {
	r.record(ctx, snapshot, false)
}

// RecordUsage publishes provider usage without changing the current phase or request.
func (r *StatusRecorder) RecordUsage(ctx context.Context, sessionID string, usage *llm.Usage) {
	if usage != nil {
		r.record(ctx, runtimestatus.Snapshot{SessionID: sessionID, Usage: usage}, true)
	}
}

func (r *StatusRecorder) record(ctx context.Context, snapshot runtimestatus.Snapshot, usageOnly bool) {
	// Status subscribers only update in-memory projections. Serialize publication
	// so a retired target cannot be recreated by an older concurrent emission.
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	ctx = r.view.Context(ctx)
	if snapshot.SessionID == "" {
		return
	}
	r.mu.Lock()
	owner, _ := contextinfo.ExecutionFromContext(ctx)
	owner.SessionID = snapshot.SessionID
	e, attempt, active := r.turns.ExecutionAttempt(snapshot.SessionID)
	previous := r.owners[snapshot.SessionID]
	terminal := snapshot.Phase == runtimestatus.PhaseDone || snapshot.Phase == runtimestatus.PhaseError
	valid := active && e == turn.ExecutionFromContext(ctx) && attempt == owner.Attempt
	if !active {
		valid = terminal && owner.Attempt != "" && previous.Attempt == owner.Attempt && previous.RunID == owner.RunID
	}
	if !valid {
		r.mu.Unlock()
		return
	}
	if usageOnly {
		usage := snapshot.Usage
		previous := r.snapshots[snapshot.SessionID]
		previous.SessionID, previous.Usage = snapshot.SessionID, usage
		snapshot = previous
	} else {
		// Phase changes never overwrite the last provider observation.
		snapshot.Usage = r.snapshots[snapshot.SessionID].Usage
		snapshot = mergeRuntimeStatus(r.snapshots[snapshot.SessionID], snapshot)
	}
	if !terminal && r.turns.Snapshot(snapshot.SessionID).Phase == turn.PhaseStopping {
		snapshot.Phase = runtimestatus.PhaseStopping
	}
	snapshot.Usage = agentevents.CloneUsage(snapshot.Usage)
	r.snapshots[snapshot.SessionID] = snapshot
	r.owners[snapshot.SessionID] = owner
	r.version++
	version := r.version
	r.mu.Unlock()
	snapshot.Usage = agentevents.CloneUsage(snapshot.Usage)
	event := agentevents.StatusChangedEvent{Snapshot: snapshot, Version: version, Execution: owner}
	if conversation, ok := contextinfo.ConversationFromContext(ctx); ok {
		event.Conversation = &conversation
	}
	if binding, ok := session.BindingFromContext(ctx); ok {
		event.Binding = &agentevents.BindingSnapshot{
			Scope: binding.Scope(), SessionID: binding.SessionID(), Version: binding.Version(),
		}
	}
	// This snapshot is committed. Publish it even when the request just ended.
	agentevents.Emit(context.WithoutCancel(ctx), r.changed, event)
}

// Usage restores the durable snapshot once; fresh observations always win.
func (r *StatusRecorder) Usage(row *storage.Session) (*llm.Usage, error) {
	if row == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.snapshots[row.ID]
	if !ok {
		state, err := contextmgr.DecodeState(row.Metadata)
		if err != nil {
			return nil, err
		}
		snapshot.SessionID, snapshot.Usage = row.ID, agentevents.CloneUsage(state.LastUsage)
		r.snapshots[row.ID] = snapshot
	}
	return agentevents.CloneUsage(snapshot.Usage), nil
}

func (r *StatusRecorder) Snapshot(sessionID string) runtimestatus.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.snapshots[sessionID]
	snapshot.Usage = agentevents.CloneUsage(snapshot.Usage)
	return snapshot
}

func mergeRuntimeStatus(previous, next runtimestatus.Snapshot) runtimestatus.Snapshot {
	if next.Provider == "" {
		next.Provider = previous.Provider
	}
	if next.Model == "" {
		next.Model = previous.Model
	}
	if next.Mode == "" {
		next.Mode = previous.Mode
	}
	if next.TurnStartedAt.IsZero() {
		next.TurnStartedAt = previous.TurnStartedAt
	}
	if next.StageStartedAt.IsZero() {
		next.StageStartedAt = previous.StageStartedAt
	}
	if next.Usage == nil {
		next.Usage = previous.Usage
	}
	if next.Phase == "" {
		next.Phase = previous.Phase
	}
	return next
}

func runtimeDoneStatus(base runtimestatus.Snapshot, usageUpdatedAt time.Time) runtimestatus.Snapshot {
	if usageUpdatedAt.IsZero() {
		usageUpdatedAt = time.Now()
	}
	base.Phase = runtimestatus.PhaseDone
	base.RequestID = ""
	base.Kind = ""
	base.Label = ""
	base.ToolName = ""
	base.StageStartedAt = base.TurnStartedAt
	base.FinishedAt = usageUpdatedAt
	base.Error = ""
	return base
}

// StatusRecorderDeps declares only the capabilities consumed by StatusRecorder.
type StatusRecorderDeps struct {
	View    dialogue.ExecutionView // Refreshes source facts after foreground adoption.
	Turns   *turn.Manager
	Changed *signal.Signal[agentevents.StatusChangedEvent, signal.Empty]
}

// NewStatusRecorder initializes StatusRecorder without installing cross-service callbacks.
func NewStatusRecorder(deps StatusRecorderDeps) *StatusRecorder {
	return &StatusRecorder{
		view:      deps.View,
		turns:     deps.Turns,
		changed:   deps.Changed,
		snapshots: make(map[string]runtimestatus.Snapshot),
		owners:    make(map[string]contextinfo.Execution),
	}
}
