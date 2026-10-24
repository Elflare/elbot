package execution

import toolshare "elbot/internal/tool/share"

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
	name string
	wait <-chan struct{}
}

func (t ruleDetailTool) Name() string          { return t.name }
func (t ruleDetailTool) Info() toolshare.Info       { return toolshare.Info{Name: t.name, Risk: toolshare.RiskSafe} }
func (ruleDetailTool) Schema() llm.ToolSchema  { return llm.ToolSchema{} }
func (t ruleDetailTool) Detail() string        { return "#skill " + t.name + " - body" }
func (ruleDetailTool) ActivateTools() []string { return nil }
func (t ruleDetailTool) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	if t.wait != nil {
		select {
		case <-t.wait:
		case <-ctx.Done():
			return toolshare.DetailBlock{}, ctx.Err()
		}
	}
	return toolshare.DetailBlock{Content: t.Detail(), Format: "elyph"}, nil
}
func (t ruleDetailTool) Call(context.Context, toolshare.CallRequest) (*toolshare.Result, string, error) {
	return &toolshare.Result{Content: t.Detail(), NeedsElyphRules: true}, "", nil
}

type ruleCommitter struct {
	testToolCommitter
	failure error
	saved   chan struct{}
}

func (c *ruleCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, message llm.LLMMessage, update *storage.ToolResultCommit) (events.LogResult, error) {
	if c.failure != nil && call.ID == "a" {
		return events.LogResult{ResultStatus: events.ResultFailed}, c.failure
	}
	logResult, err := c.testToolCommitter.Result(ctx, index, call, message, update)
	if c.saved != nil {
		close(c.saved)
		c.saved = nil
	}
	return logResult, err
}

func TestRuleCardFollowsCompletionOrderAndSurvivesLaterCommitFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	secondFinished := make(chan struct{})
	registry := tool.NewRegistry()
	for _, candidate := range []toolshare.Tool{tool.NewDiscoverTool(registry), ruleDetailTool{name: "alpha", wait: secondFinished}, ruleDetailTool{name: "beta"}} {
		if err := registry.Register(candidate); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(NewManager(registry, security.DefaultPermission()))
	row := &storage.Session{ID: "s", Mode: storage.SessionModeWork}
	failure := errors.New("result rejected")
	committer := &ruleCommitter{failure: failure, saved: secondFinished}
	result := runner.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: committer,
		Calls: []llm.ToolCallRequest{{ID: "a", Name: "discover_tool", Arguments: `{"name":"alpha"}`}, {ID: "b", Name: "discover_tool", Arguments: `{"name":"beta"}`}}})
	if !errors.Is(result.Err, failure) || len(result.Messages) != 2 {
		t.Fatalf("result=%+v", result)
	}
	first := llm.SegmentsContentText(result.Messages[0].Segments)
	second := llm.SegmentsContentText(result.Messages[1].Segments)
	if !strings.HasPrefix(first, "已发现：alpha。") || strings.Contains(first, "ELyph 规则：") || strings.Count(second, "ELyph 规则：") != 1 || strings.Index(second, "ELyph 规则：") > strings.Index(second, "#skill beta") {
		t.Fatalf("wrong ordered presentation:\n%s\n%s", first, second)
	}
	if !strings.Contains(row.Metadata, "elyph_rules_shown") {
		t.Fatalf("committed results lost state after later failure: %s", row.Metadata)
	}
	if !strings.Contains(committer.results[0].Metadata, `"elyph_rules_shown":true`) {
		t.Fatal("rules must travel with the stored message")
	}
	result = runner.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: &ruleCommitter{}, Calls: []llm.ToolCallRequest{{ID: "retry", Name: "alpha", Arguments: `{}`}}})
	if result.Err != nil || len(result.Messages) != 1 {
		t.Fatalf("retry: %+v", result)
	}
	if text := llm.SegmentsContentText(result.Messages[0].Segments); strings.Contains(text, "ELyph 规则：") {
		t.Fatalf("committed rule card repeated: %s", text)
	}
}

func TestDiscoveryListAndFailuresDoNotConsumeRules(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(ruleDetailTool{name: "alpha"})
	runner := NewRunner(NewManager(registry, security.DefaultPermission()))
	row := &storage.Session{ID: "s", Mode: storage.SessionModeWork}
	for _, args := range []string{`{}`, `{"name":"missing"}`} {
		result := runner.Run(t.Context(), &runnerTestDeps{}, toolshare.RunRequest{Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Committer: &ruleCommitter{}, Calls: []llm.ToolCallRequest{{ID: "lookup", Name: "discover_tool", Arguments: args}}})
		if result.Err != nil || strings.Contains(row.Metadata, "elyph_rules_shown") {
			t.Fatalf("lookup advanced rules: %+v %s", result, row.Metadata)
		}
	}
}
