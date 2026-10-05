package agent

import (
	"sync"
	"time"

	runtimestatus "elbot/internal/runtime"
)

// statusRecorder is the synchronous source of runtime snapshots. Display
// delivery happens after Record returns and never while its mutex is held.
type statusRecorder struct {
	mu        sync.Mutex
	snapshots map[string]runtimestatus.Snapshot
}

func (r *statusRecorder) Record(snapshot runtimestatus.Snapshot) runtimestatus.Snapshot {
	if snapshot.SessionID == "" {
		return snapshot
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshots == nil {
		r.snapshots = make(map[string]runtimestatus.Snapshot)
	}
	snapshot = mergeRuntimeStatus(r.snapshots[snapshot.SessionID], snapshot)
	r.snapshots[snapshot.SessionID] = snapshot
	return snapshot
}

func (r *statusRecorder) Snapshot(sessionID string) runtimestatus.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshots[sessionID]
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
