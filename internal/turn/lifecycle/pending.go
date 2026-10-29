// 排队输入的规范化与合并。
package lifecycle

import (
	"elbot/internal/llm"
	"fmt"
	"strings"
)

func (m *Manager) AppendPendingInput(sessionID string, input Input) bool {
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
func (m *Manager) DrainMergedInput(sessionID, attempt string) Input {
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
func appendPending(turn *state, input Input) bool {
	input = normalizeInput(input)
	if input.Text == "" && len(input.Segments) == 0 {
		return false
	}
	turn.pending = append(turn.pending, input)
	return true
}
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
