// 登记确认，并生成绑定本次等待身份的回调。
package lifecycle

import "elbot/internal/turn/share"

func (m *Manager) CancelAppend(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitAppendConfirm {
		return false
	}
	beginStopTurn(m.turns, sessionID, turn)
	return true
}
func (m *Manager) BeginRiskConfirmation(sessionID string, confirmation RiskConfirmation, attempt string) *share.RiskWait {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, []string{attempt}) || turn.phase != PhaseTool {
		return nil
	}
	turn.phase = PhaseAwaitRiskConfirm
	turn.riskConfirm = &confirmation
	turn.riskResponse = make(chan RiskDecision, 1)
	turn.waitRefresh = make(chan struct{}, 1)
	turn.riskDone = make(chan struct{})
	response := turn.riskResponse
	return &share.RiskWait{Response: response, Refresh: turn.waitRefresh, Done: turn.riskDone,
		Cancel: func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.turns[sessionID] != turn || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse != response {
				return false
			}
			beginStopTurn(m.turns, sessionID, turn)
			return true
		},
		Complete: func(decision RiskDecision, err error) RiskResult {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.turns[sessionID] != turn || turn.riskResponse != response {
				return RiskResult{Outcome: share.RiskStale}
			}
			if turn.phase == PhaseStopping || err != nil {
				beginStopTurn(m.turns, sessionID, turn)
				return RiskResult{Outcome: share.RiskCanceled, Err: err}
			}
			if turn.phase != PhaseAwaitRiskConfirm {
				return RiskResult{Outcome: share.RiskStale}
			}
			turn.riskConfirm = nil
			turn.riskResponse = nil
			turn.waitRefresh = nil
			close(turn.riskDone)
			turn.riskDone = nil
			turn.phase = PhaseTool
			return RiskResult{Decision: decision, Outcome: share.RiskAnswered}
		}}

}
func (m *Manager) ResolveRiskConfirmation(sessionID string, decision RiskDecision) bool {
	switch decision.Action {
	case share.RiskConfirm, share.RiskConfirmTool, share.RiskConfirmAll, share.RiskReject:
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
	case turn.riskResponse <- decision:
		return true
	default:
		return false
	}
}
func (m *Manager) RefreshRiskConfirmation(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return false
	}
	refreshWait(turn)
	return true
}
func (m *Manager) PendingRiskConfirmation(sessionID string) (RiskConfirmation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskConfirm == nil {
		return RiskConfirmation{}, false
	}
	return *turn.riskConfirm, true
}
func (m *Manager) BeginAppend(sessionID, attempt string, input Input) *share.AppendWait {
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
	done := turn.appendDone
	return &share.AppendWait{Refresh: turn.waitRefresh, Done: done, Cancel: func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.turns[sessionID] != turn || turn.phase != PhaseAwaitAppendConfirm || turn.appendDone != done {
			return false
		}
		beginStopTurn(m.turns, sessionID, turn)
		return true
	}}
}
