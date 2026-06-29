package toolrun

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type runnerTestDeps struct {
	confirmed bool
	recorded  []runnerTestRecord
}

type runnerTestRecord struct {
	tool string
	err  error
}

func (d *runnerTestDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	return call, nil
}

func (d *runnerTestDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return false
}

func (d *runnerTestDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (ConfirmResult, error) {
	d.confirmed = true
	return ConfirmResult{Allowed: true}, nil
}

func (d *runnerTestDeps) ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved ResolvedTool, assessment tool.RiskAssessment) (ConfirmResult, bool) {
	return ConfirmResult{}, false
}

func (d *runnerTestDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), error) {
	return ctx, time.Now(), func() {}, nil
}

func (d *runnerTestDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, result string, callErr error) (string, error) {
	return result, nil
}

func (d *runnerTestDeps) SendPreview(ctx context.Context, text string) {}

func (d *runnerTestDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	return nil
}

func (d *runnerTestDeps) RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) {
	d.recorded = append(d.recorded, runnerTestRecord{tool: call.Name, err: callErr})
}

func (d *runnerTestDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string) {
}

func (d *runnerTestDeps) RememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) {
}

func (d *runnerTestDeps) AddToolUse(sessionID, toolName string) {}

func (d *runnerTestDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return storage.Message{SessionID: sessionID, Role: storage.RoleTool, Content: llm.SegmentsContentText(message.Segments), ToolCallID: message.ToolCallID}
}

func (d *runnerTestDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content}
}

func (d *runnerTestDeps) PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage { return message }

type runnerPreflightTool struct{}

func (runnerPreflightTool) Name() string { return "preflight_tool" }

func (runnerPreflightTool) Info() tool.Info {
	return tool.Info{Name: "preflight_tool", Risk: tool.RiskHigh}
}

func (runnerPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: "preflight_tool", Parameters: map[string]any{"type": "object"}}}
}

func (runnerPreflightTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	return fmt.Errorf("preflight rejected")
}

func (runnerPreflightTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "should not call"}, nil
}

type runnerShellPreflightTool struct{}

func (runnerShellPreflightTool) Name() string { return "shell" }

func (runnerShellPreflightTool) Info() tool.Info {
	return tool.Info{Name: "shell", Risk: tool.RiskHigh}
}

func (runnerShellPreflightTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: "shell", Parameters: map[string]any{"type": "object"}}}
}

func (runnerShellPreflightTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	return tool.RiskAssessment{Level: tool.RiskHigh}, nil
}

func (runnerShellPreflightTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	return fmt.Errorf("protected file must use dedicated tool")
}

func (runnerShellPreflightTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "should not call"}, nil
}

type runnerRiskErrorTool struct{}

func (runnerRiskErrorTool) Name() string { return "risk_error" }

func (runnerRiskErrorTool) Info() tool.Info {
	return tool.Info{Name: "risk_error", Risk: tool.RiskHigh}
}

func (runnerRiskErrorTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: "risk_error", Parameters: map[string]any{"type": "object"}}}
}

func (runnerRiskErrorTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	return tool.RiskAssessment{}, fmt.Errorf("risk parser failed")
}

func (runnerRiskErrorTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "called"}, nil
}

func TestRunSkipsConfirmationWhenPreflightFails(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(runnerPreflightTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{}
	result := manager.Run(context.Background(), deps, RunRequest{
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   security.Actor{Role: security.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "preflight_tool",
			Arguments: `{}`,
		}},
	})
	if deps.confirmed {
		t.Fatal("preflight error should not ask for confirmation")
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d", len(result.Messages))
	}
	text := llm.SegmentsContentText(result.Messages[0].Segments)
	if !strings.Contains(text, "tool call preflight_tool failed") || !strings.Contains(text, "preflight rejected") {
		t.Fatalf("unexpected tool message: %s", text)
	}
	if len(deps.recorded) != 1 || deps.recorded[0].err == nil {
		t.Fatalf("expected recorded preflight error, got %#v", deps.recorded)
	}
}

func TestRunSkipsConfirmationWhenShellPreflightFails(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(runnerShellPreflightTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{}
	result := manager.Run(context.Background(), deps, RunRequest{
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   security.Actor{Role: security.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "shell",
			Arguments: `{"cmd":"echo changed > memories.toml"}`,
		}},
	})
	if deps.confirmed {
		t.Fatal("shell preflight error should not ask for confirmation")
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d", len(result.Messages))
	}
	text := llm.SegmentsContentText(result.Messages[0].Segments)
	if !strings.Contains(text, "tool call shell failed") || !strings.Contains(text, "dedicated tool") {
		t.Fatalf("unexpected tool message: %s", text)
	}
	if len(deps.recorded) != 1 || deps.recorded[0].err == nil {
		t.Fatalf("expected recorded shell preflight error, got %#v", deps.recorded)
	}
}

func TestRunKeepsNonEditFileAssessRiskBehavior(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(runnerRiskErrorTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	deps := &runnerTestDeps{}
	result := manager.Run(context.Background(), deps, RunRequest{
		Session: &storage.Session{ID: "s1", Mode: storage.SessionModeWork},
		Actor:   security.Actor{Role: security.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{
			ID:        "call-1",
			Name:      "risk_error",
			Arguments: `{}`,
		}},
	})
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d", len(result.Messages))
	}
	if got := llm.SegmentsContentText(result.Messages[0].Segments); !strings.Contains(got, "assess risk: risk parser failed") {
		t.Fatalf("tool result = %q", got)
	}
}
