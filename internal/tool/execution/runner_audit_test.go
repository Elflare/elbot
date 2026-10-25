package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
)

type blockingAuditTool struct{ entered, release chan struct{} }

func (blockingAuditTool) Name() string { return "audit_probe" }
func (blockingAuditTool) Info() toolshare.Info {
	return toolshare.Info{Name: "audit_probe", Risk: toolshare.RiskLow}
}
func (blockingAuditTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "audit_probe", Parameters: map[string]any{"type": "object"}}
}
func (b blockingAuditTool) Call(ctx context.Context, _ toolshare.CallRequest) (*toolshare.Result, string, error) {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, "cancelled", ctx.Err()
	}
	return &toolshare.Result{Content: "finished"}, "finished", nil
}

func TestAuditShowsStartBeforeToolReturns(t *testing.T) {
	center, err := logging.NewManager("debug", filepath.Join(t.TempDir(), "sessions.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	probe := blockingAuditTool{entered: make(chan struct{}), release: make(chan struct{})}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	deps := &runnerTestDeps{}
	deps.prepareContext = func(ctx context.Context, _ *storage.Session, _ llm.ToolCallRequest) context.Context { return ctx }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result := newTestManager(registry, security.NewPermission("high", "high", nil)).Run(ctx, deps, toolshare.RunRequest{
			Committer: &testToolCommitter{}, Session: &storage.Session{ID: "audit-live", Mode: storage.SessionModeWork},
			Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
			Calls: []llm.ToolCallRequest{{ID: "audit-live-call", Name: probe.Name(), Arguments: "{}"}},
		})
		done <- result.Err
	}()
	select {
	case <-probe.entered:
	case err := <-done:
		t.Fatalf("tool never entered: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("tool entry timeout")
	}
	audit := logging.Reader{Dir: center.LogDir()}
	waitFor := func(end bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			result, err := audit.Query(t.Context(), logging.LogQuery{Prefix: "audit", Days: 1, Limit: 10, MinLevel: "debug"})
			if err != nil {
				t.Fatal(err)
			}
			text := ""
			for _, entry := range result.Entries {
				text += entry.Raw
			}
			if !end && strings.Contains(text, "tool_call_completed") {
				t.Fatalf("premature end: %s", text)
			}
			if strings.Contains(text, "tool_call_prepared") && strings.Contains(text, "audit-live-call") && (!end || strings.Contains(text, "tool_call_completed") && strings.Count(text, "audit-live-call") == 2) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing audit event (end=%v): %s", end, text)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(false)
	close(probe.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool return timeout")
	}
	waitFor(true)
}
