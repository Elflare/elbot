package agent

import (
	"context"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestCompleteToolCallExposesNameAndAcceptsMultimodalSegments(t *testing.T) {
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{
		Point: hook.PointToolCallCompleted,
		Name:  "test.tool-image",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			if event.Tool.Name != "screenshot" || event.Tool.ID != "call_1" || event.Message.Role != string(llm.RoleTool) {
				t.Fatalf("event = %#v", event)
			}
			event.Message.Segments = append(event.Message.Segments, llm.MessageSegment{Type: llm.SegmentImage, URL: "https://example.com/result.png"})
			return event, nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	deps := agentToolRunDeps{agent: &Agent{platform: &fakePlatform{}, hooks: manager, scopeID: "default"}}
	segments, err := deps.CompleteToolCall(context.Background(), &storage.Session{ID: "s1"}, llm.ToolCallRequest{ID: "call_1", Name: "screenshot", Arguments: `{}`}, "low", llm.TextSegments("done"), nil)
	if err != nil {
		t.Fatalf("CompleteToolCall: %v", err)
	}
	if len(segments) != 2 || segments[1].Type != llm.SegmentImage {
		t.Fatalf("segments = %#v", segments)
	}
}
