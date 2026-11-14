package execution

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestRunPublishesBatchCallsBeforeAnyResults(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, runnerResultTool{}.Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("high", "high", nil))
	deps := &runnerTestDeps{}
	result := manager.Run(context.Background(), deps, share.RunRequest{
		Committer: &testToolCommitter{},
		Session:   &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:     contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{
			{ID: "call-1", Name: "edit_file", Arguments: `{"path":"one"}`},
			{ID: "call-2", Name: "edit_file", Arguments: `{"path":"two"}`},
		},
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if deps.toolBatches != 1 || len(deps.toolCalls) != 2 || len(deps.eventOrder) != 4 || deps.eventOrder[0] != "call:call-1" || deps.eventOrder[1] != "call:call-2" {
		t.Fatalf("tool event order = %v", deps.eventOrder)
	}
}
