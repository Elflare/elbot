// 验证 Hook 日志发布和流水线执行结果的来源语义。

package hook

import (
	"context"
	"log/slog"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestLogHookPreservesSourceRecordAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-session"}))
	cancel()
	identity := ""
	record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "hook_tool_call_completed", Module: "hook", Type: events.ContentJSON, Content: `{"result":"来源正文"}`, ContentLimit: 0, Identity: events.LogIdentity{SessionID: &identity}, ResultStatus: events.ResultFailed}
	var received events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(ctx context.Context, value events.LogRecord) error {
		if ctx.Err() != nil {
			t.Fatal("publication inherited cancellation")
		}
		received = value
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	LogHook(ctx, record)
	identity = "changed"
	if received.Content != record.Content || received.Level != slog.LevelInfo || received.ResultStatus != events.ResultFailed || received.Identity.SessionID == nil || *received.Identity.SessionID != "" || received.Identity.RunID != nil {
		t.Fatalf("source record changed: %+v", received)
	}
}
