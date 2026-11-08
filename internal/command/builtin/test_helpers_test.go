package builtin

import (
	"context"
	"fmt"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	controlformat "elbot/internal/control/format"
	"elbot/internal/fileops"
	"elbot/internal/tool"
	"testing"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func newCommandTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }

func commandResultText(result *command.Result) string {
	if result == nil {
		return "nil"
	}
	switch data := result.Data.(type) {
	case command.TextData:
		return data.Content
	case control.ListSessionsResult:
		return controlformat.Sessions(data, controlformat.SessionsOptions{MessageLimit: 4, PreviewLimit: 24})
	case control.SessionMessagesResult:
		return controlformat.Messages(data, controlformat.MessagesOptions{PreviewLimit: 40})
	case control.SessionChangeResult:
		return controlformat.SessionChange("", data, controlformat.MessagesOptions{PreviewLimit: 80})
	case control.StatusResult:
		return controlformat.Status(data)
	case control.SelectModelResult:
		return controlformat.Model(data)
	case control.ListModelsResult:
		return controlformat.Models(data)
	case control.ListRequestsResult:
		return controlformat.Requests(data, controlformat.RequestsOptions{ShowSessionTitle: true})
	case control.StopResult:
		return controlformat.Stop(data)
	case []tool.ToolInfo:
		return controlformat.Tools(data)
	case fileops.RollbackResult:
		return controlformat.Rollback(data)
	case []fileops.RollbackInfo:
		return controlformat.Rollbacks(data)
	case control.QueryLogsResult:
		return controlformat.Logs("logs", data)
	case control.QueryUsageResult:
		return controlformat.Usage(data)
	case control.CheckConfigurationResult:
		return controlformat.Doctor(data)
	case control.ListHooksResult:
		return controlformat.Hooks(data)
	case control.GetHookResult:
		return controlformat.HookDetail(data)
	case control.ReloadHooksResult:
		return controlformat.HookReload(data)
	case control.ListCommandsResult:
		return controlformat.Help("/", data)
	default:
		return fmt.Sprintf("%+v", data)
	}
}
