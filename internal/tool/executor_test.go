package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
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
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.ResultStatus != events.ResultSucceeded || !result.ExecutionStarted {
		t.Fatalf("execution facts = %#v", result)
	}
	if result.Message.Role != llm.RoleTool || result.Message.ToolCallID != "call_1" || result.Message.Name != "ok" || llm.SegmentsContentText(result.Message.Segments) != "ok" {
		t.Fatalf("message = %#v", result.Message)
	}
}

func TestExecutorMissingToolReturnsToolMessageError(t *testing.T) {
	executor := Executor{Registry: NewRegistry()}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "missing", Arguments: `{}`})
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.ResultStatus != events.ResultFailed || result.ExecutionStarted {
		t.Fatalf("lookup facts = %#v", result)
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
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if len(result.Message.Segments) != 2 || result.Message.Segments[1].Type != llm.SegmentImage || result.Message.Segments[1].URL != "https://example.com/a.png" {
		t.Fatalf("segments = %#v", result.Message.Segments)
	}
}

func TestResultWarningsAppendToContent(t *testing.T) {
	result := (&Result{Content: "ok", Warnings: []string{"use read_file"}}).LLMSegments()
	text := llm.SegmentsContentText(result)
	if !strings.Contains(text, "ok") || !strings.Contains(text, "Warnings:") || !strings.Contains(text, "- use read_file") {
		t.Fatalf("text = %q", text)
	}
}

func TestResultWarningsAppendToSegments(t *testing.T) {
	result := (&Result{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "text"}, {Type: llm.SegmentImage, URL: "https://example.com/a.png"}}, Warnings: []string{"use read_file"}}).LLMSegments()
	if len(result) != 2 || result[1].Type != llm.SegmentImage {
		t.Fatalf("segments = %#v", result)
	}
	text := llm.SegmentsContentText(result)
	if !strings.Contains(text, "text") || !strings.Contains(text, "Warnings:") || !strings.Contains(text, "use read_file") {
		t.Fatalf("text = %q", text)
	}
}

func TestExecutorDeniesToolAboveUserRisk(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "danger", source: SourceBuiltin, risk: RiskHigh}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Policy: security.DefaultPolicy()}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "danger", Arguments: `{}`})
	if result.Error == nil || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "above your allowed tool level") {
		t.Fatalf("result = %#v", result)
	}
	var denied PolicyDeniedError
	if result.ResultStatus != events.ResultRejected || result.ExecutionStarted || !errors.As(result.Error, &denied) {
		t.Fatalf("policy facts = %#v", result)
	}
}

type partialResultTool struct {
	segmentTool
	err error
}

func (p partialResultTool) Call(context.Context, CallRequest) (*Result, string, error) {
	{
		result, callErr := &Result{Content: "completed first action", Data: []byte(`{"partial":true}`)}, p.err
		logContent := ""
		if result != nil {
			logContent = result.Content
		}
		if callErr != nil {
			if logContent != "" {
				logContent += "\n"
			}
			logContent += callErr.Error()
		}
		return result, logContent, callErr
	}
}

func TestExecutorRetainsPartialResultAndOriginalError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status events.ResultStatus
	}{
		{"failure", errors.New("second action failed"), events.ResultFailed},
		{"wrapped cancellation", fmt.Errorf("interrupted: %w", context.Canceled), events.ResultCanceled},
		{"timeout", context.DeadlineExceeded, events.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(partialResultTool{err: tc.err}); err != nil {
				t.Fatal(err)
			}
			result := (Executor{Registry: registry}).Execute(context.Background(), llm.ToolCallRequest{ID: "partial", Name: "segments", Arguments: `{}`})
			if result.Error != tc.err || result.ResultStatus != tc.status || !result.ExecutionStarted {
				t.Fatalf("execution facts = %#v", result)
			}
			if result.Result == nil || string(result.Result.Data) != `{"partial":true}` || llm.SegmentsContentText(result.Message.Segments) != "completed first action" {
				t.Fatalf("partial result lost: %#v", result)
			}
		})
	}
}

func TestExecutorDeniesSuperadminOnlyToolForNormalUser(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "cron", source: SourceBuiltin, risk: RiskMedium, superadminOnly: true}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Policy: security.NewPolicy("medium", "high", nil)}
	result := executor.Execute(context.Background(), llm.ToolCallRequest{ID: "call_1", Name: "cron", Arguments: `{}`})
	if result.Error == nil || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "requires superadmin") {
		t.Fatalf("result = %#v", result)
	}
}

type segmentTool struct{}

func (segmentTool) Name() string { return "segments" }
func (segmentTool) Info() Info   { return Info{Name: "segments", Source: SourceBuiltin, Risk: RiskLow} }
func (segmentTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "segments"}
}
func (segmentTool) Call(context.Context, CallRequest) (*Result, string, error) {
	{
		result := &Result{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "image:"},
			{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
		}}
		return result, result.Content, nil
	}
}
