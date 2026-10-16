package toolrun_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type blockingAuditTool struct{ entered, release chan struct{} }

func (blockingAuditTool) Name() string    { return "audit_probe" }
func (blockingAuditTool) Info() tool.Info { return tool.Info{Name: "audit_probe", Risk: tool.RiskLow} }
func (blockingAuditTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "audit_probe", Parameters: map[string]any{"type": "object"}}
}
func (b blockingAuditTool) Call(ctx context.Context, _ tool.CallRequest) (*tool.Result, string, error) {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, "cancelled", ctx.Err()
	}
	return &tool.Result{Content: "finished"}, "finished", nil
}

func TestAuditShowsStartBeforeToolReturns(t *testing.T) {
	center, err := logging.NewManager("debug", filepath.Join(t.TempDir(), "sessions.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	probe := blockingAuditTool{entered: make(chan struct{}), release: make(chan struct{})}
	registry := tool.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	deps := &rollbackConfirmDeps{}
	deps.prepareContext = func(ctx context.Context, _ *storage.Session, _ llm.ToolCallRequest) context.Context { return ctx }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result := toolrun.NewRunner(toolrun.NewManager(registry, security.NewPolicy("high", "high", nil))).Run(ctx, deps, toolrun.RunRequest{
			Committer: deps, Session: &storage.Session{ID: "audit-live", Mode: storage.SessionModeWork},
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
	audit := commandbuiltin.NewAudit(commandbuiltin.Deps{Logs: logging.Reader{Dir: center.LogDir()}})
	waitFor := func(end bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			result, err := audit.Handle(t.Context(), command.Request{Args: "-t --raw -n 10"})
			if err != nil {
				t.Fatal(err)
			}
			text := result.Content
			if !end && strings.Contains(text, "tool_call_end") {
				t.Fatalf("premature end: %s", text)
			}
			if strings.Contains(text, "tool_call_start") && strings.Contains(text, "audit-live-call") && (!end || strings.Contains(text, "tool_call_end") && strings.Count(text, "audit-live-call") == 2) {
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
