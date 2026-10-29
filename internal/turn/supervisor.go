// 对外业务入口；状态存储和操作将在 lifecycle 实现时迁移。
package turn

import (
	"sort"
	"sync"

	"elbot/internal/llm"
)

type Supervisor struct {
	mu    sync.Mutex
	turns map[string]*state
}

func (m *Supervisor) StartCompactRun(sessionID, runID string, execution *Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.turns[sessionID]; old != nil {
		if !old.reserved || !old.compactReady || old.phase != PhaseLLM || old.execution != execution {
			return false
		}
	}
	m.turns[sessionID] = &state{phase: PhaseCompact, attempt: runID, execution: execution, tools: map[string]int{}, attemptDone: make(chan struct{})}
	return true
}

func (m *Supervisor) CompleteCompactRun(sessionID, runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || (turn.phase != PhaseCompact && turn.phase != PhaseStopping) || turn.attempt != runID {
		return false
	}
	removeTurn(m.turns, sessionID, turn, true)
	return true
}

func (m *Supervisor) AppendPendingInput(sessionID string, input Input) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || (turn.phase != PhaseAwaitAppendConfirm && turn.phase != PhaseTool) {
		return false
	}
	if appendPending(turn, input) && turn.phase == PhaseAwaitAppendConfirm {
		refreshWait(turn)
	}
	return true
}

func (m *Supervisor) CancelAppend(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitAppendConfirm {
		return false
	}
	beginStopTurn(m.turns, sessionID, turn)
	return true
}

func (m *Supervisor) BeginRiskConfirmation(sessionID string, confirmation RiskConfirmation, attempt string) *RiskConfirmationWait {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || turn.phase != PhaseTool {
		return nil
	}
	turn.phase = PhaseAwaitRiskConfirm
	turn.riskConfirm = &confirmation
	turn.riskResponse = make(chan riskResponse, 1)
	turn.waitRefresh = make(chan struct{}, 1)
	return &RiskConfirmationWait{manager: m, sessionID: sessionID, turn: turn, response: turn.riskResponse, refresh: turn.waitRefresh}
}

func (m *Supervisor) ResolveRiskConfirmation(sessionID string, decision RiskDecision) bool {
	switch decision.Action {
	case RiskConfirm, RiskConfirmTool, RiskConfirmAll, RiskReject:
	default:
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return false
	}
	select {
	case turn.riskResponse <- riskResponse{Confirmed: decision.Action != RiskReject, Rejected: decision.Action == RiskReject, ConfirmAll: decision.Action == RiskConfirmAll, ConfirmTool: decision.Action == RiskConfirmTool, Extra: decision.Extra, Reason: decision.Reason}:
		return true
	default:
		return false
	}
}

func (m *Supervisor) RefreshRiskConfirmation(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return false
	}
	refreshWait(turn)
	return true
}

func (m *Supervisor) PendingRiskConfirmation(sessionID string) (RiskConfirmation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskConfirm == nil {
		return RiskConfirmation{}, false
	}
	return *turn.riskConfirm, true
}

func (m *Supervisor) StartToolPhase(sessionID, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || turn.phase != PhaseLLM {
		return false
	}
	turn.phase = PhaseTool
	return true
}

func (m *Supervisor) CompleteTurn(sessionID, attempt string) (Input, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || (turn.phase != PhaseLLM && turn.phase != PhaseTool && turn.phase != PhaseAwaitRiskConfirm) {
		return Input{}, false
	}
	pending := mergeInputs(turn.pending)
	if turn.execution == nil {
		removeTurn(m.turns, sessionID, turn)
	} else if pending.Text != "" || len(pending.Segments) > 0 {
		finishAttempt(turn)
		m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: pending, tools: map[string]int{}, execution: turn.execution, reserved: true, compactReady: true}
		pending.Segments = append([]llm.MessageSegment(nil), pending.Segments...)
	} else {
		removeTurn(m.turns, sessionID, turn, true)
	}
	return pending, true
}

func (m *Supervisor) FinishAttempt(sessionID, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) {
		return
	}
	if turn.phase == PhaseAwaitAppendConfirm {
		finishAttempt(turn)
		return
	}
	removeTurn(m.turns, sessionID, turn)
}

func (m *Supervisor) StopAttempt(sessionID, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.turns[sessionID]
	if !matches(current, []string{attempt}) {
		return
	}
	beginStopTurn(m.turns, sessionID, current)
}

func (m *Supervisor) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sessionID, turn := range m.turns {
		beginStopTurn(m.turns, sessionID, turn)
	}
}

func (m *Supervisor) DrainMergedInput(sessionID, attempt string) Input {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || len(turn.pending) == 0 {
		return Input{}
	}
	merged := mergeInputs(turn.pending)
	turn.pending = nil
	return merged
}

func (m *Supervisor) AddToolUse(sessionID, name, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) {
		return
	}
	turn.tools[name]++
}

func (m *Supervisor) Snapshot(sessionID string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok {
		return Snapshot{SessionID: sessionID, Phase: PhaseIdle}
	}
	return snapshot(sessionID, turn)
}

func (m *Supervisor) SnapshotAll() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.turns))
	for sessionID, turn := range m.turns {
		out = append(out, snapshot(sessionID, turn))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

func (m *Supervisor) ResumeAppend(sessionID string) (Input, *Execution, bool) {
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
	s.originalInput.Segments = append([]llm.MessageSegment(nil), input.Segments...)
	s.pending = nil
	s.attempt = ""
	s.reserved = true
	return input, e, true
}

func (m *Supervisor) StartAttempt(sessionID string, input Input, e *Execution, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.turns[sessionID]; old != nil && !(old.reserved && old.execution == e && old.phase == PhaseLLM) {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, attempt: attempt, attemptDone: make(chan struct{})}
	return true
}

func (m *Supervisor) ReserveExecution(sessionID string, input Input, e *Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turns[sessionID] != nil {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, reserved: true}
	return true
}

func (m *Supervisor) CanCompact(sessionID string, e *Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.turns[sessionID]
	return s == nil || (s.reserved && s.compactReady && s.phase == PhaseLLM && s.execution == e)
}
func (m *Supervisor) StopSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.turns[sessionID]; current != nil {
		beginStopTurn(m.turns, sessionID, current)
	}
}
func (m *Supervisor) BeginAppend(sessionID, attempt string, input Input) *AppendWait {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || turn.phase != PhaseLLM {
		return nil
	}
	turn.phase = PhaseAwaitAppendConfirm
	turn.waitRefresh = make(chan struct{}, 1)
	turn.appendDone = make(chan struct{})
	appendPending(turn, input)
	return &AppendWait{manager: m, sessionID: sessionID, state: turn, refresh: turn.waitRefresh, done: turn.appendDone}
}
