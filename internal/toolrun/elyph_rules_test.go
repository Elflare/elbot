package toolrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type ruleDetailTool struct {
	name    string
	wait    <-chan struct{}
	release chan<- struct{}
}

func (t ruleDetailTool) Name() string          { return t.name }
func (t ruleDetailTool) Info() tool.Info       { return tool.Info{Name: t.name, Risk: tool.RiskSafe} }
func (ruleDetailTool) Schema() llm.ToolSchema  { return llm.ToolSchema{} }
func (t ruleDetailTool) Detail() string        { return "#skill " + t.name + " - body" }
func (ruleDetailTool) ActivateTools() []string { return nil }
func (t ruleDetailTool) LoadDetail(ctx context.Context) (tool.DetailBlock, error) {
	if t.release != nil {
		close(t.release)
	}
	if t.wait != nil {
		select {
		case <-t.wait:
		case <-ctx.Done():
			return tool.DetailBlock{}, ctx.Err()
		}
	}
	return tool.DetailBlock{Content: t.Detail(), Format: "elyph"}, nil
}
func (t ruleDetailTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	return &tool.Result{Content: t.Detail(), NeedsElyphRules: true}, "", nil
}

type ruleCommitter struct {
	testToolCommitter
	failure error
}

func (c *ruleCommitter) Flush(context.Context) (events.LogResult, error) {
	return events.LogResult{}, c.failure
}

func TestRuleCardFollowsCallOrderAndCommittedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	secondFinished := make(chan struct{})
	registry := tool.NewRegistry()
	for _, candidate := range []tool.Tool{tool.NewDiscoverTool(registry), ruleDetailTool{name: "alpha", wait: secondFinished}, ruleDetailTool{name: "beta", release: secondFinished}} {
		if err := registry.Register(candidate); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(NewManager(registry, security.DefaultPolicy()))
	row := &storage.Session{ID: "s", Mode: storage.SessionModeWork}
	failure := errors.New("batch rejected")
	committer := &ruleCommitter{failure: failure}
	result := runner.Run(ctx, &runnerTestDeps{}, RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: committer,
		Calls: []llm.ToolCallRequest{{ID: "a", Name: "discover_tool", Arguments: `{"name":"alpha"}`}, {ID: "b", Name: "discover_tool", Arguments: `{"name":"beta"}`}}})
	if !errors.Is(result.Err, failure) || len(result.Messages) != 2 {
		t.Fatalf("result=%+v", result)
	}
	first := llm.SegmentsContentText(result.Messages[0].Segments)
	second := llm.SegmentsContentText(result.Messages[1].Segments)
	if !strings.HasPrefix(first, "已发现：alpha。") || strings.Count(first, "ELyph 规则：") != 1 || strings.Index(first, "ELyph 规则：") > strings.Index(first, "#skill alpha") || strings.Contains(second, "ELyph 规则：") {
		t.Fatalf("wrong ordered presentation:\n%s\n%s", first, second)
	}
	if strings.Contains(row.Metadata, "elyph_rules_shown") {
		t.Fatalf("failed batch advanced state: %s", row.Metadata)
	}
	if !strings.Contains(committer.results[0].Metadata, `"elyph_rules_shown":true`) {
		t.Fatal("rules must travel with the stored message")
	}
	// Retry through a dynamic ordinary result, then reuse the committed session.
	for attempt := 0; attempt < 2; attempt++ {
		result = runner.Run(ctx, &runnerTestDeps{}, RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: &ruleCommitter{}, Calls: []llm.ToolCallRequest{{ID: "retry", Name: "alpha", Arguments: `{}`}}})
		if result.Err != nil || len(result.Messages) != 1 {
			t.Fatalf("retry: %+v", result)
		}
		text := llm.SegmentsContentText(result.Messages[0].Segments)
		if strings.Contains(text, "ELyph 规则：") != (attempt == 0) {
			t.Fatalf("attempt %d: %s", attempt, text)
		}
	}
}

func TestDiscoveryListAndFailuresDoNotConsumeRules(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(ruleDetailTool{name: "alpha"})
	runner := NewRunner(NewManager(registry, security.DefaultPolicy()))
	row := &storage.Session{ID: "s", Mode: storage.SessionModeWork}
	for _, args := range []string{`{}`, `{"name":"missing"}`} {
		result := runner.Run(t.Context(), &runnerTestDeps{}, RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: &ruleCommitter{}, Calls: []llm.ToolCallRequest{{ID: "lookup", Name: "discover_tool", Arguments: args}}})
		if result.Err != nil || strings.Contains(row.Metadata, "elyph_rules_shown") {
			t.Fatalf("lookup advanced rules: %+v %s", result, row.Metadata)
		}
	}
}
