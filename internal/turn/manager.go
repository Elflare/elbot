package turn

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Phase string

const (
	PhaseIdle               Phase = "idle"
	PhaseLLM                Phase = "llm"
	PhaseTool               Phase = "tool"
	PhaseAwaitRiskConfirm   Phase = "awaiting_risk_confirm"
	PhaseAwaitAppendConfirm Phase = "awaiting_append_confirm"
	PhaseCompact            Phase = "compact"
)

type RiskConfirmation struct {
	ID        string
	ToolName  string
	Arguments string
	Risk      string
	Summary   string
	Detail    string
}

type RiskConfirmationResponse struct {
	Confirmed   bool
	Rejected    bool
	Stopped     bool
	ConfirmAll  bool
	ConfirmTool bool
	Extra       string
	Reason      string
}

type Snapshot struct {
	SessionID    string
	Phase        Phase
	PendingCount int
	Tools        map[string]int
}

type Manager struct {
	mu    sync.Mutex
	turns map[string]*state
}

type state struct {
	phase         Phase
	originalInput string
	pending       []string
	riskConfirm   *RiskConfirmation
	riskResponse  chan RiskConfirmationResponse
	tools         map[string]int
}

func NewManager() *Manager {
	return &Manager{turns: map[string]*state{}}
}

func (m *Manager) StartCompact(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.turns[sessionID]; exists {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseCompact, tools: map[string]int{}}
	return true
}

func (m *Manager) CompleteCompact(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseCompact {
		return false
	}
	delete(m.turns, sessionID)
	return true
}

func (m *Manager) StartLLM(sessionID, input string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.turns[sessionID]; exists {
		return false
	}
	m.turns[sessionID] = &state{phase: PhaseLLM, originalInput: strings.TrimSpace(input), tools: map[string]int{}}
	return true
}

func (m *Manager) InterruptLLM(sessionID, input string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseLLM {
		return false
	}
	turn.phase = PhaseAwaitAppendConfirm
	appendPending(turn, input)
	return true
}

func (m *Manager) AppendPending(sessionID, input string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || (turn.phase != PhaseAwaitAppendConfirm && turn.phase != PhaseTool) {
		return false
	}
	appendPending(turn, input)
	return true
}

func (m *Manager) ConfirmAppend(sessionID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitAppendConfirm {
		return "", false
	}
	merged := mergeInputs(append([]string{turn.originalInput}, turn.pending...))
	delete(m.turns, sessionID)
	return merged, true
}

func (m *Manager) CancelAppend(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitAppendConfirm {
		return false
	}
	delete(m.turns, sessionID)
	return true
}

func (m *Manager) AwaitRiskConfirmation(sessionID string, confirmation RiskConfirmation) (RiskConfirmationResponse, bool) {
	ch := make(chan RiskConfirmationResponse, 1)
	m.mu.Lock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseTool {
		m.mu.Unlock()
		return RiskConfirmationResponse{Stopped: true}, false
	}
	turn.phase = PhaseAwaitRiskConfirm
	turn.riskConfirm = &confirmation
	turn.riskResponse = ch
	m.mu.Unlock()

	resp := <-ch
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok = m.turns[sessionID]
	if !ok {
		return resp, false
	}
	turn.riskConfirm = nil
	turn.riskResponse = nil
	if resp.Stopped {
		delete(m.turns, sessionID)
		return resp, false
	}
	turn.phase = PhaseTool
	return resp, true
}

func (m *Manager) ResolveRiskConfirmation(sessionID string, resp RiskConfirmationResponse) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return false
	}
	turn.riskResponse <- resp
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

func (m *Manager) StartToolPhase(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase != PhaseLLM {
		return false
	}
	turn.phase = PhaseTool
	return true
}

func (m *Manager) CompleteLLM(sessionID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || (turn.phase != PhaseLLM && turn.phase != PhaseTool && turn.phase != PhaseAwaitRiskConfirm) {
		return "", false
	}
	pending := mergeInputs(turn.pending)
	delete(m.turns, sessionID)
	return pending, true
}

func (m *Manager) FinishRequest(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || turn.phase == PhaseAwaitAppendConfirm {
		return
	}
	stopTurn(turn)
	delete(m.turns, sessionID)
}

func (m *Manager) StopSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stopTurn(m.turns[sessionID])
	delete(m.turns, sessionID)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, turn := range m.turns {
		stopTurn(turn)
	}
	m.turns = map[string]*state{}
}

func (m *Manager) DrainMerged(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok || len(turn.pending) == 0 {
		return ""
	}
	merged := mergeInputs(turn.pending)
	turn.pending = nil
	return merged
}

func (m *Manager) AddToolUse(sessionID, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if turn == nil {
		turn = &state{phase: PhaseTool, tools: map[string]int{}}
		m.turns[sessionID] = turn
	}
	if turn.tools == nil {
		turn.tools = map[string]int{}
	}
	turn.tools[name]++
}

func (m *Manager) Snapshot(sessionID string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn, ok := m.turns[sessionID]
	if !ok {
		return Snapshot{SessionID: sessionID, Phase: PhaseIdle}
	}
	return snapshot(sessionID, turn)
}

func (m *Manager) SnapshotAll() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.turns))
	for sessionID, turn := range m.turns {
		out = append(out, snapshot(sessionID, turn))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

func IsConfirm(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "$", "是", "y", "yes":
		return true
	default:
		return false
	}
}

func IsCancel(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "取消", "否", "n", "no":
		return true
	default:
		return false
	}
}

func ToolsString(tools map[string]int) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s x%d", name, tools[name]))
	}
	return strings.Join(parts, ", ")
}

func appendPending(turn *state, input string) {
	input = strings.TrimSpace(input)
	if input != "" {
		turn.pending = append(turn.pending, input)
	}
}

func mergeInputs(inputs []string) string {
	clean := []string{}
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input != "" {
			clean = append(clean, input)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	if len(clean) == 1 {
		return clean[0]
	}
	var sb strings.Builder
	sb.WriteString("补充信息：")
	for i, input := range clean {
		sb.WriteString(fmt.Sprintf("\n%d. %s", i+1, input))
	}
	return sb.String()
}

func stopTurn(turn *state) {
	if turn == nil || turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return
	}
	select {
	case turn.riskResponse <- RiskConfirmationResponse{Stopped: true}:
	default:
	}
}

func snapshot(sessionID string, turn *state) Snapshot {
	tools := map[string]int{}
	for name, count := range turn.tools {
		tools[name] = count
	}
	return Snapshot{SessionID: sessionID, Phase: turn.phase, PendingCount: len(turn.pending), Tools: tools}
}
