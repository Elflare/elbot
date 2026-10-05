package elnis

import (
	"context"
	"slices"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/toolrun"
)

func TestBackgroundTagAuthorizationAndToolDeclarations(t *testing.T) {
	for _, denied := range []bool{false, true} {
		runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":""}`}
		service, cleanup := newTestServiceWithRunner(t, runner, nil)
		defer cleanup()
		registry := tool.NewRegistry()
		_ = registry.Register(builtin.NewWebSearchTool())
		_ = registry.Register(builtin.NewWebExtractTool())
		members := []string{"web_search"}
		if denied {
			members = append(members, "web_extract")
		}
		service.toolPreloader = toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"research": {Tools: members}}}})
		var queued QueuedLLMEvent
		service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
		req := testRequest(ModeLLM)
		req.Elwisp.Name = "configured"
		req.ToolListNames = []string{"research"}
		req.Tools = []toolrun.ELwispToolDeclaration{testExternalTool("external")}
		_, err := service.Handle(context.Background(), "secret", req)
		if denied {
			if err == nil || !strings.Contains(err.Error(), "web_extract") {
				t.Fatalf("unauthorized tag member: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
			t.Fatal(err)
		}
		if len(runner.requests) != 1 {
			t.Fatalf("requests=%+v", runner.requests)
		}
		got := runner.requests[0]
		if !slices.Equal(got.ToolListNames, []string{"research"}) || !slices.Equal(got.AllowedToolNames, []string{"web_search"}) || len(got.CachedTools) != 1 {
			t.Fatalf("tools=%+v", got)
		}
		if got.ModelProvider != "provider-work" || got.Model != "model-work" {
			t.Fatalf("default model=%s/%s", got.ModelProvider, got.Model)
		}
	}
}
