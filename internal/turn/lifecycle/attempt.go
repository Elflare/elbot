// 执行准入、续跑、停止和退出确认。
package lifecycle

import (
	"context"
	"elbot/internal/llm"
)

func (m *Manager) StartCompactRun(sessionID, runID string, execution Execution) bool {
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
func (m *Manager) CompleteCompactRun(sessionID, runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || (turn.phase != PhaseCompact && turn.phase != PhaseStopping) || turn.attempt != runID {
		return false
	}
	removeTurn(m.turns, sessionID, turn, true)
	return true
}
func (m *Manager) StartToolPhase(sessionID, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, attempt) || turn.phase != PhaseLLM {
		return false
	}
	turn.phase = PhaseTool
	return true
}
func (m *Manager) CompleteTurn(sessionID, attempt string) (Input, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, attempt) || (turn.phase != PhaseLLM && turn.phase != PhaseTool && turn.phase != PhaseAwaitRiskConfirm) {
		return Input{}, false
	}
	pending := mergeInputs(turn.pending)
	if turn.execution == nil {
		removeTurn(m.turns, sessionID, turn, false)
	} else if pending.Text != "" || len(pending.Segments) > 0 {
		finishAttempt(turn)
		m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: pending, tools: map[string]int{}, execution: turn.execution, reserved: true, compactReady: true}
		pending.Segments = append([]llm.MessageSegment(nil), pending.Segments...)
	} else {
		removeTurn(m.turns, sessionID, turn, true)
	}
	return pending, true
}
func (m *Manager) FinishAttempt(sessionID, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, attempt) {
		return
	}
	if turn.phase == PhaseAwaitAppendConfirm {
		finishAttempt(turn)
		return
	}
	removeTurn(m.turns, sessionID, turn, false)
}
func (m *Manager) StopAttempt(sessionID, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.turns[sessionID]
	if !matches(current, attempt) {
		return
	}
	beginStopTurn(m.turns, sessionID, current)
}
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sessionID, turn := range m.turns {
		beginStopTurn(m.turns, sessionID, turn)
	}
}
func (m *Manager) ResumeAppend(sessionID string) (Input, Execution, bool) {
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
func (m *Manager) StartAttempt(sessionID string, input Input, e Execution, attempt string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.turns[sessionID]; old != nil && !(old.reserved && old.execution == e && old.phase == PhaseLLM) {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, attempt: attempt, attemptDone: make(chan struct{})}
	return true
}
func (m *Manager) ReserveExecution(sessionID string, input Input, e Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turns[sessionID] != nil {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: normalizeInput(input), tools: map[string]int{}, execution: e, reserved: true}
	return true
}
func (m *Manager) CanCompact(sessionID string, e Execution) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.turns[sessionID]
	return s == nil || (s.reserved && s.compactReady && s.phase == PhaseLLM && s.execution == e)
}
func (m *Manager) StopSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.turns[sessionID]; current != nil {
		beginStopTurn(m.turns, sessionID, current)
	}
}
func refreshWait(turn *state) {
	select {
	case turn.waitRefresh <- struct{}{}:
	default:
	}
}
func removeTurn(turns map[string]*state, sessionID string, turn *state, preserve bool) {
	if turn.execution != nil && !preserve {
		turn.execution.Finish(context.Canceled)
	}
	finishAttempt(turn)
	stopTurn(turn)
	if turn.appendDone != nil {
		close(turn.appendDone)
		turn.appendDone = nil
	}
	delete(turns, sessionID)
}
func finishAttempt(turn *state) {
	if turn.attemptDone != nil {
		close(turn.attemptDone)
		turn.attemptDone = nil
	}
}
func beginStopTurn(turns map[string]*state, sessionID string, turn *state) {
	if turn.attemptDone == nil {
		removeTurn(turns, sessionID, turn, false)
		return
	}
	stopTurn(turn)
	turn.phase = PhaseStopping
	turn.pending = nil
	if turn.appendDone != nil {
		close(turn.appendDone)
		turn.appendDone = nil
	}
}
func stopTurn(turn *state) {
	if turn.riskDone != nil {
		close(turn.riskDone)
		turn.riskDone = nil
	}
}
