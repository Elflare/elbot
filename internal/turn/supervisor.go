// 对外业务入口，委托生命周期组件持有状态。
package turn

import (
	"context"
	"elbot/internal/storage"
	"elbot/internal/turn/lifecycle"
	"elbot/internal/turn/share"
)

type Supervisor struct {
	lifecycle *lifecycle.Manager
	next      storage.IDSequence // 唯一执行和 attempt 发号能力，不持有数据库。
}

func (m *Supervisor) NewExecution(ctx context.Context) (*Execution, error) {
	id, err := m.next.ID(ctx, "run")
	if err != nil {
		return nil, err
	}
	return NewExecution(id), nil
}

func (m *Supervisor) AllocateAttempt(ctx context.Context) (string, error) { return m.next.ID(ctx, "a") }

func (m *Supervisor) StartCompactRun(sessionID, runID string, execution *Execution) bool {
	return m.lifecycle.StartCompactRun(sessionID, runID, executionContract(execution))
}
func (m *Supervisor) CompleteCompactRun(sessionID, runID string) bool {
	return m.lifecycle.CompleteCompactRun(sessionID, runID)
}
func (m *Supervisor) AppendPendingInput(sessionID string, input Input) bool {
	return m.lifecycle.AppendPendingInput(sessionID, lifecycle.Input(input))
}
func (m *Supervisor) BeginRiskConfirmation(sessionID string, confirmation RiskConfirmation, attempt string) *RiskConfirmationWait {
	w := m.lifecycle.BeginRiskConfirmation(sessionID, toLifecycleRiskConfirmation(confirmation), attempt)
	if w == nil {
		return nil
	}
	return &RiskConfirmationWait{wait: w}
}
func (m *Supervisor) ResolveRiskConfirmation(sessionID string, decision RiskDecision) bool {
	return m.lifecycle.ResolveRiskConfirmation(sessionID, share.RiskDecision{Action: share.RiskAction(decision.Action), Extra: decision.Extra, Reason: decision.Reason})
}
func (m *Supervisor) RefreshRiskConfirmation(sessionID string) bool {
	return m.lifecycle.RefreshRiskConfirmation(sessionID)
}
func (m *Supervisor) PendingRiskConfirmation(sessionID string) (RiskConfirmation, bool) {
	v, ok := m.lifecycle.PendingRiskConfirmation(sessionID)
	return fromLifecycleRiskConfirmation(v), ok
}

func toLifecycleRiskConfirmation(confirmation RiskConfirmation) lifecycle.RiskConfirmation {
	detail := make([]lifecycle.ConfirmationDetailBlock, len(confirmation.Detail))
	for i, block := range confirmation.Detail {
		detail[i] = lifecycle.ConfirmationDetailBlock{Kind: string(block.Kind), Content: block.Content}
	}
	return lifecycle.RiskConfirmation{ToolName: confirmation.ToolName, Risk: confirmation.Risk, Detail: detail}
}

func fromLifecycleRiskConfirmation(confirmation lifecycle.RiskConfirmation) RiskConfirmation {
	detail := make([]ConfirmationDetailBlock, len(confirmation.Detail))
	for i, block := range confirmation.Detail {
		detail[i] = ConfirmationDetailBlock{Kind: ConfirmationDetailKind(block.Kind), Content: block.Content}
	}
	return RiskConfirmation{ToolName: confirmation.ToolName, Risk: confirmation.Risk, Detail: detail}
}
func (m *Supervisor) StartToolPhase(sessionID, attempt string) bool {
	return m.lifecycle.StartToolPhase(sessionID, attempt)
}
func (m *Supervisor) CompleteTurn(sessionID, attempt string) (Input, bool) {
	input, ok := m.lifecycle.CompleteTurn(sessionID, attempt)
	return Input(input), ok
}
func (m *Supervisor) FinishAttempt(sessionID, attempt string) {
	m.lifecycle.FinishAttempt(sessionID, attempt)
}
func (m *Supervisor) StopAttempt(sessionID, attempt string) {
	m.lifecycle.StopAttempt(sessionID, attempt)
}
func (m *Supervisor) StopAll() { m.lifecycle.StopAll() }
func (m *Supervisor) DrainMergedInput(sessionID, attempt string) Input {
	return Input(m.lifecycle.DrainMergedInput(sessionID, attempt))
}
func (m *Supervisor) AddToolUse(sessionID, name, attempt string) {
	m.lifecycle.AddToolUse(sessionID, name, attempt)
}
func (m *Supervisor) Snapshot(sessionID string) Snapshot {
	return publicSnapshot(m.lifecycle.Snapshot(sessionID))
}
func (m *Supervisor) SnapshotAll() []Snapshot {
	snapshots := m.lifecycle.SnapshotAll()
	out := make([]Snapshot, len(snapshots))
	for i, s := range snapshots {
		out[i] = publicSnapshot(s)
	}
	return out
}
func (m *Supervisor) ResumeAppend(sessionID string) (Input, *Execution, bool) {
	input, e, ok := m.lifecycle.ResumeAppend(sessionID)
	return Input(input), publicExecution(e), ok
}
func (m *Supervisor) StartAttempt(sessionID string, input Input, e *Execution, attempt string) bool {
	return m.lifecycle.StartAttempt(sessionID, lifecycle.Input(input), executionContract(e), attempt)
}
func (m *Supervisor) ReserveExecution(sessionID string, input Input, e *Execution) bool {
	return m.lifecycle.ReserveExecution(sessionID, lifecycle.Input(input), executionContract(e))
}
func (m *Supervisor) CanCompact(sessionID string, e *Execution) bool {
	return m.lifecycle.CanCompact(sessionID, executionContract(e))
}
func (m *Supervisor) StopSession(sessionID string) { m.lifecycle.StopSession(sessionID) }
func (m *Supervisor) BeginAppend(sessionID, attempt string, input Input) bool {
	return m.lifecycle.BeginAppend(sessionID, attempt, lifecycle.Input(input))
}

func (m *Supervisor) ExpireAppend(sessionID string, waitID uint64) bool {
	return m.lifecycle.ExpireAppend(sessionID, waitID)
}
