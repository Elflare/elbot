package tool

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
)

func TestExecutorExecutesTool(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "ok", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "ok", Arguments: `{}`})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Message.Role != llm.RoleTool || result.Message.ToolCallID != "call_1" || result.Message.Name != "ok" || llm.SegmentsContentText(result.Message.Segments) != "ok" {
		t.Fatalf("message = %#v", result.Message)
	}
}

func TestExecutorMissingToolReturnsToolMessageError(t *testing.T) {
	executor := Executor{Registry: NewRegistry()}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "missing", Arguments: `{}`})
	if result.Err == nil {
		t.Fatal("expected error")
	}
	if result.Message.Role != llm.RoleTool || result.Message.ToolCallID != "call_1" || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "not found") {
		t.Fatalf("message = %#v", result.Message)
	}
}

func TestExecutorUsesResultSegments(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(segmentTool{}); err != nil {
		t.Fatal(err)
	}
	result := Executor{Registry: registry}.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "segments", Arguments: `{}`})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Message.Segments) != 2 || result.Message.Segments[1].Type != llm.SegmentImage || result.Message.Segments[1].URL != "https://example.com/a.png" {
		t.Fatalf("segments = %#v", result.Message.Segments)
	}
}

func TestExecutorDeniesToolAboveUserRisk(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "danger", source: SourceBuiltin, risk: RiskHigh}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry, Actor: security.Actor{Role: security.RoleUser}, Policy: security.DefaultPolicy()}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "danger", Arguments: `{}`})
	if result.Err == nil || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "above your allowed tool level") {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecutorDeniesSuperadminOnlyToolForNormalUser(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "cron", source: SourceBuiltin, risk: RiskMedium, superadminOnly: true}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry, Actor: security.Actor{Role: security.RoleUser}, Policy: security.NewPolicy("medium", "high", nil)}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "cron", Arguments: `{}`})
	if result.Err == nil || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "requires superadmin") {
		t.Fatalf("result = %#v", result)
	}
}

type segmentTool struct{}

func (segmentTool) Name() string { return "segments" }
func (segmentTool) Info() Info   { return Info{Name: "segments", Source: SourceBuiltin, Risk: RiskLow} }
func (segmentTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Function: llm.ToolFunctionSchema{Name: "segments"}}
}
func (segmentTool) Call(context.Context, CallRequest) (*Result, error) {
	return &Result{Segments: []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "image:"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
	}}, nil
}
