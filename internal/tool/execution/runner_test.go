package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
)

type runnerContextKey struct{}

type testToolCommitter struct {
	head    *storage.Message
	results []storage.Message
	records map[string]storage.ToolCallRecord
}

func (c *testToolCommitter) Begin(_ context.Context, head *storage.Message) (events.LogResult, error) {
	c.head = head
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (*testToolCommitter) Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *testToolCommitter) Result(_ context.Context, _ int, _ llm.ToolCallRequest, _ llm.LLMMessage, row *storage.ToolResultCommit) (events.LogResult, error) {
	c.results = append(c.results, *row.Result)
	if row.Record != nil {
		if c.records == nil {
			c.records = map[string]storage.ToolCallRecord{}
		}
		c.records[row.Record.ToolCallID] = *row.Record
	}
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

func (*testToolCommitter) Flush(context.Context) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

type runnerTestDeps struct {
	confirmed      bool
	completed      int
	prepareContext func(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
	reject         bool
}

func (d *runnerTestDeps) RefreshExecution(ctx context.Context, _ *storage.Session) (context.Context, error) {
	return ctx, nil
}

func (d *runnerTestDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error) {
	return call, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
}

func (d *runnerTestDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return false
}

func (d *runnerTestDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment toolshare.RiskAssessment, detail string) (toolshare.ConfirmResult, error) {
	d.confirmed = true
	if d.reject {
		return toolshare.ConfirmResult{Message: toolMessage(call.Name, call.ID, "rejected")}, nil
	}
	return toolshare.ConfirmResult{Allowed: true}, nil
}

func (d *runnerTestDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), events.LogResult, error) {
	return ctx, time.Now(), func() {}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (d *runnerTestDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	if d.prepareContext != nil {
		return d.prepareContext(ctx, session, call)
	}
	return ctx
}

func (d *runnerTestDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, events.LogResult, error) {
	d.completed++
	return segments, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
}

func (d *runnerTestDeps) SendPreview(ctx context.Context, text ...string) {}

func (d *runnerTestDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (d *runnerTestDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk toolshare.RiskLevel, reason string) {
}

func (d *runnerTestDeps) AddToolUse(sessionID, toolName string) {}

func (d *runnerTestDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	raw, _ := json.Marshal(message.Segments)
	return storage.Message{SessionID: sessionID, Role: storage.RoleTool, Content: llm.SegmentsContentText(message.Segments), Segments: string(raw), ToolCallID: message.ToolCallID}
}

func (d *runnerTestDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content}
}

type runnerPreflightTool struct{}

func (runnerPreflightTool) Name() string { return "preflight_tool" }

func (runnerPreflightTool) Info() toolshare.Info {
	return toolshare.Info{Name: "preflight_tool", Risk: toolshare.RiskHigh}
}

func (runnerPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "preflight_tool", Parameters: map[string]any{"type": "object"}}
}

func (runnerPreflightTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return nil, fmt.Errorf("preflight rejected")
}

type runnerContextPreflightTool struct{}

func (runnerContextPreflightTool) Name() string { return "context_preflight" }

func (runnerContextPreflightTool) Info() toolshare.Info {
	return toolshare.Info{Name: "context_preflight", Risk: toolshare.RiskLow}
}

func (runnerContextPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "context_preflight", Parameters: map[string]any{"type": "object"}}
}

func (runnerContextPreflightTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	if ctx.Value(runnerContextKey{}) != "prepared" {
		return nil, fmt.Errorf("missing prepared context")
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskLow}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		if ctx.Value(runnerContextKey{}) != "prepared" {
			{
				callErr := fmt.Errorf("missing prepared context during call")
				return nil, callErr.Error(), callErr
			}
		}
		{
			result := &toolshare.Result{Content: "called"}
			return result, result.Content, nil
		}
	}), nil
}

type runnerImageTool struct{}

func (runnerImageTool) Name() string { return "image_tool" }

func (runnerImageTool) Info() toolshare.Info {
	return toolshare.Info{Name: "image_tool", Risk: toolshare.RiskLow}
}

func (runnerImageTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "image_tool", Parameters: map[string]any{"type": "object"}}
}

func (value runnerImageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Segments: []llm.MessageSegment{
				{Type: llm.SegmentText, Text: "done"},
				{Type: llm.SegmentImage, URL: "https://example.com/a.png"},
			}}
			return result, result.Content, nil
		}
	}), nil
}

type runnerHighRiskTool struct {
	name        string
	ownerScoped bool
}

func (t runnerHighRiskTool) Name() string { return t.name }

func (t runnerHighRiskTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Risk: toolshare.RiskHigh, OwnerScoped: t.ownerScoped}
}

func (t runnerHighRiskTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}

func (value runnerHighRiskTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Content: "called"}
			return result, result.Content, nil
		}
	}), nil
}

func TestRunConfirmsOnlyAuthorizedHighRiskToolsForRegularUsers(t *testing.T) {
	tests := []struct {
		name        string
		userMaxRisk string
		ownerScoped bool
		wantConfirm bool
		wantCalled  bool
	}{
		{name: "owner scoped", userMaxRisk: "low", ownerScoped: true, wantConfirm: true, wantCalled: true},
		{name: "unauthorized non-owner", userMaxRisk: "low", wantConfirm: false, wantCalled: false},
		{name: "authorized non-owner", userMaxRisk: "high", wantConfirm: true, wantCalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			toolName := "high_risk_" + strings.ReplaceAll(tt.name, " ", "_")
			if err := registry.Register(runnerHighRiskTool{name: toolName, ownerScoped: tt.ownerScoped}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission(tt.userMaxRisk, "high", nil))
			deps := &runnerTestDeps{}
			result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: &testToolCommitter{},
				Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
				Actor:   contextinfo.Actor{Role: contextinfo.RoleUser},
				Calls:   []llm.ToolCallRequest{{ID: "call-1", Name: toolName, Arguments: "{}"}},
			})
			if deps.confirmed != tt.wantConfirm {
				t.Fatalf("confirmed = %v, want %v", deps.confirmed, tt.wantConfirm)
			}
			gotCalled := len(result.Messages) == 1 && llm.SegmentsContentText(result.Messages[0].Segments) == "called"
			if gotCalled != tt.wantCalled {
				t.Fatalf("called = %v, want %v; messages = %#v", gotCalled, tt.wantCalled, result.Messages)
			}
		})
	}
}
func TestRunPreservesMultimodalToolSegments(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(runnerImageTool{}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("low", "high", map[string][]string{"cli": {"local"}}))
	result := manager.Run(context.Background(), &runnerTestDeps{}, toolshare.RunRequest{Committer: &testToolCommitter{},
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls:   []llm.ToolCallRequest{{ID: "call-1", Name: "image_tool", Arguments: `{}`}},
	})
	if len(result.Messages) != 1 || len(result.Messages[0].Segments) != 2 || result.Messages[0].Segments[1].Type != llm.SegmentImage {
		t.Fatalf("messages = %#v", result.Messages)
	}
}

func TestRunSkipsConfirmationWhenPreflightFails(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(runnerPreflightTool{}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{}
	committer := &testToolCommitter{}
	result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer,
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "preflight_tool",
			Arguments: `{}`,
		}},
	})
	if deps.confirmed {
		t.Fatal("preflight error should not ask for confirmation")
	}
	if deps.completed != 0 {
		t.Fatalf("completed hook calls = %d, want 0", deps.completed)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d", len(result.Messages))
	}
	text := llm.SegmentsContentText(result.Messages[0].Segments)
	if !strings.Contains(text, "tool call preflight_tool failed") || !strings.Contains(text, "preflight rejected") {
		t.Fatalf("unexpected tool message: %s", text)
	}
	if len(committer.records) != 1 || committer.records["call-1"].Success {
		t.Fatalf("expected recorded preflight error, got %#v", committer.records)
	}
}

func TestRunPreparesToolContextBeforePreflightAndExecution(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(runnerContextPreflightTool{}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{prepareContext: func(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
		return context.WithValue(ctx, runnerContextKey{}, "prepared")
	}}
	result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: &testToolCommitter{},
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "context_preflight",
			Arguments: `{}`,
		}},
	})
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d", len(result.Messages))
	}
	if got := llm.SegmentsContentText(result.Messages[0].Segments); got != "called" {
		t.Fatalf("tool result = %q", got)
	}
}

func TestRunPreservesPolicyDenialFact(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registry.Register(runnerHighRiskTool{name: "risky"}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("low", "high", nil))
			deps := &runnerTestDeps{reject: reject}
			role := contextinfo.RoleUser
			if reject {
				role = contextinfo.RoleSuperadmin
			}
			committer := &testToolCommitter{}
			result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer, Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: role}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "risky", Arguments: `{}`}}})
			if result.Err != nil || len(committer.records) != 1 || deps.completed != 0 {
				t.Fatalf("result=%+v deps=%+v", result, deps)
			}
			if committer.records["call"].Success || committer.records["call"].Error == "" {
				t.Fatalf("missing denial fact: %v", committer.records)
			}
		})
	}
}
