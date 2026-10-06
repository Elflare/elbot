package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func TestUnregisteredNativeDialogueRejectsBeforeCompactionAndInputWrite(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("unsupported dialogue reached upstream")
	}))
	defer srv.Close()
	client, err := responses.New(srv.URL, "key", nil, nil, responses.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t)
	fixture := newExecutionFixture(t, &fakeLLM{}, store)
	models := newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"native": client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response"}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}, "chat": {Provider: "native", Model: "m"}}})
	fixture.execution.models = models
	ctx, row, err := fixture.execution.resolveInput(context.Background(), "incoming")
	if err != nil {
		t.Fatal(err)
	}
	old := &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "existing history"}
	if err := store.Messages().Append(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.opts.Contexts.RecordUsage(ctx, row.ID, &llm.Usage{PromptTokens: 1000000000, TotalTokens: 1000000000}); err != nil {
		t.Fatal(err)
	}
	next, _, err := fixture.execution.runAttempt(ctx, row, "incoming", fixture.out)
	if err == nil || !strings.Contains(err.Error(), "尚未接入") {
		t.Fatalf("error=%v", err)
	}
	if next.ID != row.ID {
		t.Fatal("unsupported route compacted the session")
	}
	messages, err := store.Messages().ListBySession(ctx, row.ID)
	if err != nil || len(messages) != 1 || messages[0].ID != old.ID {
		t.Fatalf("messages=%+v error=%v", messages, err)
	}
	if calls.Load() != 0 || len(fixture.opts.Requests.ListBySession(row.ID)) != 0 {
		t.Fatal("unsupported dialogue created a model request")
	}
}
