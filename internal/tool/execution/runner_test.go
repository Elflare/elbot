package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
)

type runnerContextKey struct{}

type testToolCommitter struct {
	head     *storage.Message
	messages map[int]llm.LLMMessage
	results  []storage.Message
	records  map[string]storage.ToolCallRecord
	flushes  int
}

func (c *testToolCommitter) Begin(_ context.Context, head *storage.Message) error {
	c.head = head
	c.messages = make(map[int]llm.LLMMessage)
	return nil
}
func (*testToolCommitter) Prepared(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *testToolCommitter) Result(_ context.Context, index int, _ llm.ToolCallRequest, message llm.LLMMessage, row *storage.ToolResultCommit) error {
	c.messages[index] = message
	c.results = append(c.results, *row.Result)
	if row.Record != nil {
		if c.records == nil {
			c.records = map[string]storage.ToolCallRecord{}
		}
		c.records[row.Record.ToolCallID] = *row.Record
	}
	return nil
}

func (c *testToolCommitter) Flush(context.Context) error {
	c.flushes++
	return nil
}

type runnerTestDeps struct {
	confirmed      bool
	completed      int
	toolResult     delivery.ToolResult
	toolResultCall string
	prepareContext func(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
	reject         bool
}

func (d *runnerTestDeps) RefreshExecution(ctx context.Context, _ *storage.Session) (context.Context, error) {
	return ctx, nil
}

func (d *runnerTestDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	return call, nil
}

func (d *runnerTestDeps) ShouldSendPreview(context.Context, string) bool {
	return false
}

func (d *runnerTestDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment toolshare.RiskAssessment, detail []toolshare.ConfirmationDetailBlock) (operationresult.Result[toolshare.ConfirmResult], error) {
	d.confirmed = true
	if d.reject {
		return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Rejected, Message: "rejected"}, nil
	}
	return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Success}, nil
}

func (d *runnerTestDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (d *runnerTestDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	if d.prepareContext != nil {
		return d.prepareContext(ctx, session, call)
	}
	return ctx
}

func (d *runnerTestDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, result operationresult.Result[[]llm.MessageSegment], callErr error) ([]llm.MessageSegment, error) {
	d.completed++
	if callErr != nil {
		return llm.TextSegments(callErr.Error()), nil
	}
	if result.Status != operationresult.Success {
		return llm.TextSegments(result.Message), nil
	}
	return result.Data, nil
}

func (d *runnerTestDeps) SendPreview(ctx context.Context, text ...string) {}

func (d *runnerTestDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) (operationresult.Result[struct{}], error) {
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (d *runnerTestDeps) PublishToolResult(_ context.Context, callID string, result delivery.ToolResult) {
	d.toolResultCall = callID
	d.toolResult = result
}

func (d *runnerTestDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk toolshare.RiskLevel, reason string) {
}

func (d *runnerTestDeps) AddToolUse(sessionID, toolName string) {}

func (d *runnerTestDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	raw, _ := json.Marshal(message.Segments)
	return storage.Message{ID: storage.NewID(), SessionID: sessionID, Role: storage.RoleTool, Content: llm.SegmentsContentText(message.Segments), Segments: string(raw), ToolCallID: message.ToolCallID}
}

func (d *runnerTestDeps) ToolCallMessage(sessionID, content, rawText, reasoningContent string, calls []llm.ToolCallRequest) storage.Message {
	return storage.Message{ID: storage.NewID(), SessionID: sessionID, Role: storage.RoleAssistant, Content: content}
}

type runnerPreflightTool struct{}

func (runnerPreflightTool) Name() string { return "preflight_tool" }

func (runnerPreflightTool) Info() toolshare.Info {
	return toolshare.Info{Name: "preflight_tool", Risk: toolshare.RiskHigh}
}

func (runnerPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "preflight_tool", Parameters: map[string]any{"type": "object"}}
}

func (runnerPreflightTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Rejected, Message: "preflight rejected"}, nil
}

type runnerContextPreflightTool struct{}

func (runnerContextPreflightTool) Name() string { return "context_preflight" }

func (runnerContextPreflightTool) Info() toolshare.Info {
	return toolshare.Info{Name: "context_preflight", Risk: toolshare.RiskLow}
}

func (runnerContextPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "context_preflight", Parameters: map[string]any{"type": "object"}}
}

func (runnerContextPreflightTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	if ctx.Value(runnerContextKey{}) != "prepared" {
		return operationresult.Result[*toolshare.PreparedCall]{}, fmt.Errorf("missing prepared context")
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskLow}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		if ctx.Value(runnerContextKey{}) != "prepared" {
			{
				callErr := fmt.Errorf("missing prepared context during call")
				return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
			}
		}

		result := &toolshare.Result{Content: "called"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil

	})}, nil
}

type runnerImageTool struct{}

type runnerResultTool struct{}

func (runnerResultTool) Name() string { return "edit_file" }
func (runnerResultTool) Info() toolshare.Info {
	return toolshare.Info{Name: "edit_file", Risk: toolshare.RiskLow}
}
func (runnerResultTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "edit_file", Parameters: map[string]any{"type": "object"}}
}
func (t runnerResultTool) Definition() toolshare.ToolDefinition {
	return executionTestDefinition(t)
}
func (runnerResultTool) PrepareCall(_ context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskLow}, nil, func(context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: "@@ -1 +1 @@\n-old\n+new\n"}}, "edited", nil
	})}, nil
}

func TestRunPersistsAndPublishesCommittedToolResult(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, runnerResultTool{}.Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("high", "high", nil))
	deps := &runnerTestDeps{}
	committer := &testToolCommitter{}
	result := manager.Run(context.Background(), deps, toolshare.RunRequest{
		Committer: committer, Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call-edit", Name: "edit_file", Arguments: "{}"}},
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if deps.toolResultCall != "call-edit" || deps.toolResult.Name != "edit_file" || !deps.toolResult.Success || len(deps.toolResult.Segments) != 1 || deps.toolResult.Segments[0].Text != "@@ -1 +1 @@\n-old\n+new\n" {
		t.Fatalf("published result: call=%q result=%+v", deps.toolResultCall, deps.toolResult)
	}
	if len(committer.results) != 1 {
		t.Fatalf("stored result metadata = %+v", committer.results)
	}
}

func (runnerImageTool) Name() string { return "image_tool" }

func (runnerImageTool) Info() toolshare.Info {
	return toolshare.Info{Name: "image_tool", Risk: toolshare.RiskLow}
}

func (runnerImageTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "image_tool", Parameters: map[string]any{"type": "object"}}
}

func (value runnerImageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {

		result := &toolshare.Result{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "done"},
			{Type: llm.SegmentImage, URL: "https://example.com/a.png"},
		}}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil

	})}, nil
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

func (value runnerHighRiskTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {

		result := &toolshare.Result{Content: "called"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil

	})}, nil
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
			if err := registerTestTools(registry, (runnerHighRiskTool{name: toolName, ownerScoped: tt.ownerScoped}).Definition()); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission(tt.userMaxRisk, "high", nil))
			deps := &runnerTestDeps{}
			committer := &testToolCommitter{}
			result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer,
				Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
				Actor:   contextinfo.Actor{Role: contextinfo.RoleUser},
				Calls:   []llm.ToolCallRequest{{ID: "call-1", Name: toolName, Arguments: "{}"}},
			})
			if deps.confirmed != tt.wantConfirm {
				t.Fatalf("confirmed = %v, want %v", deps.confirmed, tt.wantConfirm)
			}
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			gotCalled := len(committer.messages) == 1 && llm.SegmentsContentText(committer.messages[0].Segments) == "called"
			if gotCalled != tt.wantCalled {
				t.Fatalf("called = %v, want %v; messages = %#v", gotCalled, tt.wantCalled, committer.messages)
			}
		})
	}
}
func TestRunPreservesMultimodalToolSegments(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, (runnerImageTool{}).Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("low", "high", map[string][]string{"cli": {"local"}}))
	committer := &testToolCommitter{}
	result := manager.Run(context.Background(), &runnerTestDeps{}, toolshare.RunRequest{Committer: committer,
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls:   []llm.ToolCallRequest{{ID: "call-1", Name: "image_tool", Arguments: `{}`}},
	})
	if result.Err != nil || len(committer.messages) != 1 || len(committer.messages[0].Segments) != 2 || committer.messages[0].Segments[1].Type != llm.SegmentImage {
		t.Fatalf("result = %+v, messages = %#v", result, committer.messages)
	}
}

func TestRunSkipsConfirmationWhenPreflightFails(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, (runnerPreflightTool{}).Definition()); err != nil {
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
	if result.Err != nil || len(committer.messages) != 1 {
		t.Fatalf("result = %+v, messages = %d", result, len(committer.messages))
	}
	text := llm.SegmentsContentText(committer.messages[0].Segments)
	if text != "preflight rejected" {
		t.Fatalf("unexpected tool message: %s", text)
	}
	if len(committer.records) != 1 || committer.records["call-1"].Success {
		t.Fatalf("expected recorded preflight error, got %#v", committer.records)
	}
}

func TestRunPreparesToolContextBeforePreflightAndExecution(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, (runnerContextPreflightTool{}).Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{prepareContext: func(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
		return context.WithValue(ctx, runnerContextKey{}, "prepared")
	}}
	committer := &testToolCommitter{}
	result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer,
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "context_preflight",
			Arguments: `{}`,
		}},
	})
	if result.Err != nil || len(committer.messages) != 1 {
		t.Fatalf("result = %+v, messages = %d", result, len(committer.messages))
	}
	if got := llm.SegmentsContentText(committer.messages[0].Segments); got != "called" {
		t.Fatalf("tool result = %q", got)
	}
}

func TestRunPreservesPolicyDenialFact(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registerTestTools(registry, (runnerHighRiskTool{name: "risky"}).Definition()); err != nil {
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
