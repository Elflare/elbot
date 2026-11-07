package builtin

import (
	"context"
	command "elbot/internal/command/share"
	platformshare "elbot/internal/platform/share"
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
	return platformshare.FormatCommandResult(result)
}
