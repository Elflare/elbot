package agent

import (
	"context"
	"time"

	runtimestatus "elbot/internal/runtime"
)

func (a *Agent) updateRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	foregroundTurnOutput{agent: a}.PublishRuntimeStatus(ctx, snapshot)
}

func (a *Agent) recordRuntimeStatus(snapshot runtimestatus.Snapshot) runtimestatus.Snapshot {
	if snapshot.SessionID == "" {
		return snapshot
	}
	a.statusMu.Lock()
	previous := a.runtimeStatus[snapshot.SessionID]
	snapshot = mergeRuntimeStatus(previous, snapshot)
	a.runtimeStatus[snapshot.SessionID] = snapshot
	a.statusMu.Unlock()
	return snapshot
}

func (a *Agent) publishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) {
	snapshot = a.recordRuntimeStatus(snapshot)
	if snapshot.SessionID == "" {
		return
	}
	_ = a.dispatcher.SetRuntimeStatus(ctx, snapshot)
}

func (a *Agent) runtimeStatusForSession(sessionID string) runtimestatus.Snapshot {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.runtimeStatus[sessionID]
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
