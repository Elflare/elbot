package input

import (
	"testing"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

func TestAppendConfirmationTokens(t *testing.T) {
	probe := &commandExecutorProbe{
		row:     &storage.Session{ID: "s"},
		current: operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: control.CurrentConfirmationResult{Kind: "append"}},
		respond: operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success},
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: command.NewRouter([]string{"/"}), Sessions: probe, Identity: probe, Output: probe, Confirmations: probe})
	for _, text := range []string{"y", " Y ", "more input"} {
		_, handled, err := executor.Handle(t.Context(), text)
		if err != nil || handled != (text != "more input") {
			t.Errorf("Handle(%q): handled=%t err=%v", text, handled, err)
		}
	}
}
