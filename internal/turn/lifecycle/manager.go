// 持有唯一的状态表与锁，提供一致快照和计数。
package lifecycle

import (
	"elbot/internal/llm"
	"elbot/internal/turn/share"
	"sort"
	"sync"
)

type RiskDecision = share.RiskDecision
type RiskResult = share.RiskResult

type state struct {
	execution     Execution
	compactReady  bool
	reserved      bool
	attempt       string // Current request or compaction attempt; empty while reserved for continuation.
	phase         Phase
	originalInput Input
	pending       []Input
	riskConfirm   *RiskConfirmation
	riskResponse  chan RiskDecision
	riskDone      chan struct{}
	waitRefresh   chan struct{}
	appendDone    chan struct{}
	tools         map[string]int
	// attemptDone belongs to this attempt and survives cancellation until its owner finishes.
	attemptDone chan struct{}
}
type Manager struct {
	mu    sync.Mutex
	turns map[string]*state
}

func NewManager() *Manager { return &Manager{turns: map[string]*state{}} }
func (m *Manager) AddToolUse(sessionID, name, attempt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turn := m.turns[sessionID]
	if !matches(turn, attempt) {
		return
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
func snapshot(sessionID string, turn *state) Snapshot {
	tools := map[string]int{}
	for name, count := range turn.tools {
		tools[name] = count
	}
	return Snapshot{SessionID: sessionID, Phase: turn.phase, PendingCount: len(turn.pending), Tools: tools, AttemptDone: turn.attemptDone, Execution: turn.execution, Attempt: turn.attempt}
}
func matches(turn *state, expected string) bool {
	return turn != nil && turn.attempt == expected
}

type Phase string

const (
	PhaseIdle               Phase = "idle"
	PhaseLLM                Phase = "llm"
	PhaseTool               Phase = "tool"
	PhaseAwaitRiskConfirm   Phase = "awaiting_risk_confirm"
	PhaseAwaitAppendConfirm Phase = "awaiting_append_confirm"
	PhaseCompact            Phase = "compact"
	PhaseStopping           Phase = "stopping"
)

type Input struct {
	// NeedsElyphRules travels with queued input until its destination session is known.
	NeedsElyphRules bool
	Text            string
	PlatformText    string
	Segments        []llm.MessageSegment
}
type Snapshot struct {
	SessionID    string
	Phase        Phase
	PendingCount int
	Tools        map[string]int
	// AttemptDone closes when the current attempt has finished its actual work.
	// It is nil when a confirmation is waiting without an active attempt.
	AttemptDone <-chan struct{}
	Execution   Execution // Logical execution owning this snapshot.
	Attempt     string    // Current attempt; empty during a continuation reservation.
}
type RiskConfirmation struct {
	ID        string
	ToolName  string
	Arguments string
	Risk      string
	Summary   string
	Detail    string
}
type Execution interface{ Finish(error) }
