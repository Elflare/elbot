package cli

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIStopDisablesInputButRetainsOutput(t *testing.T) {
	input, stop := context.WithCancel(t.Context())
	m := tuiModel{ctx: t.Context(), inputCtx: input, input: newTUIInput(), width: 80, height: 24, stop: stop, handler: func(context.Context, string) error { t.Error("submitted after stop"); return nil }}
	m.input.SetValue("/exit")
	model, cmd := m.submitInput()
	if cmd != nil || input.Err() == nil {
		t.Fatal("local exit destroyed UI or did not stop input")
	}
	m = model.(tuiModel)
	model, _ = m.Update(tuiOutputMsg("draining output"))
	m = model.(tuiModel)
	if !strings.Contains(m.content, "draining output") {
		t.Fatal(m.content)
	}
	m.input.SetValue("late input")
	if _, cmd := m.submitInput(); cmd != nil {
		t.Fatal("submitted after stop")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.(tuiModel).input.Value() != "late input" {
		t.Fatal("stopped input was processed")
	}
}

func TestTUIReadinessAndOutputWaitCancellation(t *testing.T) {
	calls := 0
	m := tuiModel{ready: func() { calls++ }}
	model, _ := m.Update(tuiReadyMsg{})
	model.(tuiModel).Update(tuiReadyMsg{})
	if calls != 1 {
		t.Fatal(calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := waitTUIOutput(ctx, make(chan tea.Msg))(); got != nil {
		t.Fatal(got)
	}
}
