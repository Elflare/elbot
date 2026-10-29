// 待删除：当前混合了承载状态、输入处理、确认等待与公开定义等职责。
// 将这些职责迁入已建立的对应文件后删除本文件。
package turn

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"elbot/internal/events"
	"elbot/internal/llm"
)

type state struct {
	execution     *Execution
	compactReady  bool
	reserved      bool
	attempt       string // Current request or compaction attempt; empty while reserved for continuation.
	phase         Phase
	originalInput Input
	pending       []Input
	riskConfirm   *RiskConfirmation
	riskResponse  chan riskResponse
	waitRefresh   chan struct{}
	appendDone    chan struct{}
	tools         map[string]int
	// attemptDone belongs to this attempt and survives cancellation until its owner finishes.
	attemptDone chan struct{}
}

// Wait returns true only for expiry. Application cancellation discards this
// wait silently; timeout <= 0 still waits for resolution or shutdown.
func (w *AppendWait) Wait(ctx context.Context, timeout time.Duration) bool {
	if w == nil {
		return false
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}
	remove := func() bool {
		m := w.manager
		m.mu.Lock()
		defer m.mu.Unlock()
		current := m.turns[w.sessionID]
		if current == w.state && current.phase == PhaseAwaitAppendConfirm && current.appendDone == w.done {
			beginStopTurn(m.turns, w.sessionID, current)
			return true
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			remove()
			return false
		case <-w.done:
			return false
		case <-w.refresh:
			if timer != nil {
				resetTimer(timer, timeout)
			}
		case <-timerC:
			removed := remove()
			return removed && ctx.Err() == nil
		}
	}
}

// Cancel removes only this wait; a later confirmation on the same Turn is safe.
func (w *RiskConfirmationWait) Cancel() bool {
	if w == nil {
		return false
	}
	m := w.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.turns[w.sessionID]; current == w.turn && current.phase == PhaseAwaitRiskConfirm && current.riskResponse == w.response {
		beginStopTurn(m.turns, w.sessionID, current)
		return true
	}
	return false
}

// Wait retains the original response/acceptance contract and declares its own
// terminal result before stop, expiry and stale-wait facts collapse into false.
func (w *RiskConfirmationWait) wait(ctx context.Context, timeout time.Duration) (resp riskResponse, ok bool, resultStatus events.ResultStatus) {
	if w == nil {
		return riskResponse{Stopped: true}, false, events.ResultSkipped
	}
	defer w.Cancel()
	defer func() {
		if resultStatus == events.ResultCanceled && ctx.Err() == context.DeadlineExceeded {
			resultStatus = events.ResultFailed
		}
	}()
	if ctx.Err() != nil {
		return riskResponse{Stopped: true}, false, events.ResultCanceled
	}
	m := w.manager
	var timer *time.Timer
	var timerC <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}

	for {
		select {
		case resp := <-w.response:
			m.mu.Lock()
			current := m.turns[w.sessionID]
			if current != w.turn || (current.phase != PhaseAwaitRiskConfirm && current.phase != PhaseStopping) || current.riskResponse != w.response {
				m.mu.Unlock()
				return resp, false, events.ResultSkipped
			}
			if current.phase == PhaseStopping || resp.Stopped || ctx.Err() != nil {
				beginStopTurn(m.turns, w.sessionID, current)
				m.mu.Unlock()
				resp.Stopped = true
				return resp, false, events.ResultCanceled
			}
			current.riskConfirm = nil
			current.riskResponse = nil
			current.waitRefresh = nil
			current.phase = PhaseTool
			m.mu.Unlock()
			resultStatus := events.ResultSucceeded
			if resp.Rejected {
				resultStatus = events.ResultRejected
			}
			return resp, true, resultStatus
		case <-w.refresh:
			if timer != nil {
				resetTimer(timer, timeout)
			}
		case <-timerC:
			removed := w.Cancel()
			resultStatus := events.ResultSkipped
			if removed && ctx.Err() == nil {
				resultStatus = events.ResultFailed
			} else if ctx.Err() != nil {
				resultStatus = events.ResultCanceled
			}
			return riskResponse{Stopped: true, Expired: removed && ctx.Err() == nil}, false, resultStatus
		case <-ctx.Done():
			return riskResponse{Stopped: true}, false, events.ResultCanceled
		}
	}
}

// StopSession keeps an active attempt reserved until its owner acknowledges
// completion. Waiters use Snapshot.AttemptDone to join that attempt.

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

func appendPending(turn *state, input Input) bool {
	input = normalizeInput(input)
	if input.Text == "" && len(input.Segments) == 0 {
		return false
	}
	turn.pending = append(turn.pending, input)
	return true
}

// mergeInputs combines inputs already normalized at admission or enqueue time.
func mergeInputs(inputs []Input) Input {
	clean := []Input{}
	for _, input := range inputs {
		if input.Text != "" || len(input.Segments) > 0 {
			clean = append(clean, input)
		}
	}
	if len(clean) == 0 {
		return Input{}
	}
	if len(clean) == 1 {
		return clean[0]
	}
	segments := llm.TextSegments("补充信息：")
	platformTexts := make([]string, 0, len(clean))
	needsElyphRules := false
	for i, input := range clean {
		needsElyphRules = needsElyphRules || input.NeedsElyphRules
		segments = llm.AppendSegmentText(segments, fmt.Sprintf("\n%d. ", i+1))
		segments = append(segments, input.Segments...)
		platformTexts = append(platformTexts, input.PlatformText)
	}
	return Input{Text: llm.SegmentsTextOnly(segments), PlatformText: mergeTextInputs(platformTexts), Segments: segments, NeedsElyphRules: needsElyphRules}
}

func normalizeInput(input Input) Input {
	input.Text = strings.TrimSpace(input.Text)
	input.PlatformText = strings.TrimSpace(input.PlatformText)
	input.Segments = append([]llm.MessageSegment(nil), input.Segments...)
	if len(input.Segments) == 0 {
		input.Segments = llm.TextSegments(input.Text)
	}
	if input.Text == "" {
		input.Text = strings.TrimSpace(llm.SegmentsTextOnly(input.Segments))
	}
	return input
}

func mergeTextInputs(inputs []string) string {
	clean := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if input = strings.TrimSpace(input); input != "" {
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

func refreshWait(turn *state) {
	select {
	case turn.waitRefresh <- struct{}{}:
	default:
	}
}

func resetTimer(timer *time.Timer, timeout time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(timeout)
}

func removeTurn(turns map[string]*state, sessionID string, turn *state, preserve ...bool) {
	if turn.execution != nil && (len(preserve) == 0 || !preserve[0]) {
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
		removeTurn(turns, sessionID, turn)
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
	if turn.phase != PhaseAwaitRiskConfirm || turn.riskResponse == nil {
		return
	}
	select {
	case turn.riskResponse <- riskResponse{Stopped: true}:
	default:
	}
}

func snapshot(sessionID string, turn *state) Snapshot {
	tools := map[string]int{}
	for name, count := range turn.tools {
		tools[name] = count
	}
	return Snapshot{SessionID: sessionID, Phase: turn.phase, PendingCount: len(turn.pending), Tools: tools, AttemptDone: turn.attemptDone, Execution: turn.execution, Attempt: turn.attempt}
}

func matches(turn *state, expected []string) bool {
	return turn != nil && (len(expected) == 0 || turn.attempt == expected[0])
}

type riskResponse struct {
	Confirmed   bool
	Rejected    bool
	Stopped     bool
	Expired     bool
	ConfirmAll  bool
	ConfirmTool bool
	Extra       string
	Reason      string
}
