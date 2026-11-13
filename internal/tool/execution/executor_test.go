package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
)

func TestExecutorExecutesTool(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, (fakeTool{name: "ok", source: toolshare.SourceBuiltin}).Definition()); err != nil {
		t.Fatal(err)
	}
	result := executeTestCall(registry, llm.ToolCallRequest{ID: "call_1", Name: "ok", Arguments: `{}`})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.Error != nil || !result.ExecutionStarted {
		t.Fatalf("execution facts = %#v", result)
	}
	if result.Message.Role != llm.RoleTool || result.Message.ToolCallID != "call_1" || result.Message.Name != "ok" || llm.SegmentsContentText(result.Message.Segments) != "ok" {
		t.Fatalf("message = %#v", result.Message)
	}
}

func TestExecutorMissingToolReturnsToolMessageError(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	result := executeTestCall(registry, llm.ToolCallRequest{ID: "call_1", Name: "missing", Arguments: `{}`})
	if result.Error != nil || result.Result.Status != operationresult.NotFound {
		t.Fatal("expected error")
	}
	if result.Error != nil || result.ExecutionStarted {
		t.Fatalf("lookup facts = %#v", result)
	}
	if result.Message.Role != llm.RoleTool || result.Message.ToolCallID != "call_1" || !strings.Contains(llm.SegmentsContentText(result.Message.Segments), "not found") {
		t.Fatalf("message = %#v", result.Message)
	}
}

func TestExecutorUsesResultSegments(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, (segmentTool{}).Definition()); err != nil {
		t.Fatal(err)
	}
	result := executeTestCall(registry, llm.ToolCallRequest{ID: "call_1", Name: "segments", Arguments: `{}`})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if len(result.Message.Segments) != 2 || result.Message.Segments[1].Type != llm.SegmentImage || result.Message.Segments[1].URL != "https://example.com/a.png" {
		t.Fatalf("segments = %#v", result.Message.Segments)
	}
}

func TestResultWarningsAppendToContent(t *testing.T) {
	result := (&toolshare.Result{Content: "ok", Warnings: []string{"use read_file"}}).LLMSegments()
	text := llm.SegmentsContentText(result)
	if !strings.Contains(text, "ok") || !strings.Contains(text, "Warnings:") || !strings.Contains(text, "- use read_file") {
		t.Fatalf("text = %q", text)
	}
}

func TestResultWarningsAppendToSegments(t *testing.T) {
	result := (&toolshare.Result{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "text"}, {Type: llm.SegmentImage, URL: "https://example.com/a.png"}}, Warnings: []string{"use read_file"}}).LLMSegments()
	if len(result) != 2 || result[1].Type != llm.SegmentImage {
		t.Fatalf("segments = %#v", result)
	}
	text := llm.SegmentsContentText(result)
	if !strings.Contains(text, "text") || !strings.Contains(text, "Warnings:") || !strings.Contains(text, "use read_file") {
		t.Fatalf("text = %q", text)
	}
}

func TestRunRejectsUnauthorizedTools(t *testing.T) {
	for _, candidate := range []fakeTool{
		{name: "danger", risk: toolshare.RiskHigh},
		{name: "admin", risk: toolshare.RiskLow, superadminOnly: true},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			permission := security.DefaultPermission()
			catalog := toolregistry.NewRegistry(permission)
			if err := registerTestTools(catalog, candidate.Definition()); err != nil {
				t.Fatal(err)
			}
			deps := &runnerTestDeps{}
			sink := &testToolCommitter{}
			result := newTestManager(catalog, permission).Run(t.Context(), deps, toolshare.RunRequest{
				Committer: sink, Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork},
				Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Calls: []llm.ToolCallRequest{{ID: "one", Name: candidate.name, Arguments: "{}"}},
			})
			if result.Err != nil || deps.confirmed || deps.completed != 0 || len(sink.records) != 1 || sink.records["one"].Success || sink.records["one"].Error == "" {
				t.Fatalf("result=%+v deps=%+v records=%+v", result, deps, sink.records)
			}
		})
	}
}

type segmentTool struct{}

func (segmentTool) Name() string { return "segments" }
func (segmentTool) Info() toolshare.Info {
	return toolshare.Info{Name: "segments", Source: toolshare.SourceBuiltin, Risk: toolshare.RiskLow}
}
func (segmentTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "segments"}
}
func (value segmentTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {

		result := &toolshare.Result{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "image:"},
			{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
		}}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil

	})}, nil
}
