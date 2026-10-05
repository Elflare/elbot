package agent

import (
	"context"
	"sync"
	"time"

	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

// statusRecorder is the synchronous source of runtime snapshots. Display
// delivery happens after Record returns and never while its mutex is held.
type statusRecorder struct {
	publishMu sync.Mutex
	mu        sync.Mutex
	snapshots map[string]runtimestatus.Snapshot
	owners    map[string]EventMeta
	version   uint64
	turns     *turn.Manager
	changed   *signal.Signal[StatusChangedEvent]
}

func (r *statusRecorder) Record(ctx context.Context, snapshot runtimestatus.Snapshot, display bool) {
	// Status subscribers only update in-memory projections. Serialize publication
	// so a retired target cannot be recreated by an older concurrent emission.
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	if snapshot.SessionID == "" {
		return
	}
	r.mu.Lock()
	owner := eventMeta(ctx, snapshot.SessionID)
	if r.turns != nil {
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
	}
	if r.snapshots == nil {
		r.snapshots = make(map[string]runtimestatus.Snapshot)
		r.owners = make(map[string]EventMeta)
	}
	snapshot = mergeRuntimeStatus(r.snapshots[snapshot.SessionID], snapshot)
	snapshot.Usage = cloneUsage(snapshot.Usage)
	r.snapshots[snapshot.SessionID] = snapshot
	r.owners[snapshot.SessionID] = owner
	r.version++
	version := r.version
	r.mu.Unlock()
	snapshot.Usage = cloneUsage(snapshot.Usage)
	emitFact(ctx, r.changed, StatusChangedEvent{Snapshot: snapshot, Version: version, Display: display})
}

func (r *statusRecorder) Snapshot(sessionID string) runtimestatus.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.snapshots[sessionID]
	snapshot.Usage = cloneUsage(snapshot.Usage)
	return snapshot
}

func (a *Agent) RuntimeStatus(sessionID string) runtimestatus.Snapshot {
	return a.status.Snapshot(sessionID)
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
