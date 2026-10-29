// 装配组件并转接公开句柄和内部契约。
package turn

import (
	"context"
	"elbot/internal/turn/confirmation"
	"elbot/internal/turn/lifecycle"
	"elbot/internal/turn/share"
	"time"
)

func NewSupervisor() *Supervisor { return &Supervisor{lifecycle: lifecycle.NewManager()} }
func executionContract(e *Execution) lifecycle.Execution {
	if e == nil {
		return nil
	}
	return e
}
func publicExecution(e lifecycle.Execution) *Execution {
	if e == nil {
		return nil
	}
	return e.(*Execution)
}
func publicSnapshot(s lifecycle.Snapshot) Snapshot {
	return Snapshot{SessionID: s.SessionID, Phase: Phase(s.Phase), PendingCount: s.PendingCount, Tools: s.Tools, AttemptDone: s.AttemptDone, Execution: publicExecution(s.Execution), Attempt: s.Attempt}
}
func (w *AppendWait) Wait(ctx context.Context, timeout time.Duration) bool {
	if w == nil {
		return false
	}
	return confirmation.WaitAppend(ctx, w.wait, timeout)
}
func (w *RiskConfirmationWait) Cancel() bool { return w != nil && w.wait.Cancel() }
func (w *RiskConfirmationWait) Wait(ctx context.Context, timeout time.Duration) RiskResult {
	var wait *share.RiskWait
	if w != nil {
		wait = w.wait
	}
	r := confirmation.WaitRisk(ctx, wait, timeout)
	return RiskResult{Decision: RiskDecision{Action: RiskAction(r.Decision.Action), Extra: r.Decision.Extra, Reason: r.Decision.Reason}, Outcome: RiskOutcome(r.Outcome), Err: r.Err}
}
