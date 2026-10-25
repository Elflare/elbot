package elnis

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"slices"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/registry"
)

func TestBackgroundTagAuthorizationAndToolDeclarations(t *testing.T) {
	for _, denied := range []bool{false, true} {
		runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":""}`}
		service, cleanup := newTestServiceWithRunner(t, runner, nil)
		defer cleanup()
		registry := registry.NewRegistry(security.DefaultPermission())
		_ = registry.Register(backgroundDeclarationTool{name: "web_search"})
		_ = registry.Register(backgroundDeclarationTool{name: "web_extract"})
		members := []string{"web_search"}
		if denied {
			members = append(members, "web_extract")
		}
		service.toolPreloader = preload.NewPreloadService(preload.PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"research": {Tools: members}}}})
		var queued QueuedLLMEvent
		service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
		req := testRequest(ModeLLM)
		req.Elwisp.Name = "configured"
		req.ToolListNames = []string{"research"}
		req.Tools = []toolshare.ELwispToolDeclaration{testExternalTool("external")}
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
		if !slices.Equal(got.ToolListNames, []string{"research"}) || !slices.Equal(got.AllowedToolNames, []string{"web_search"}) || len(got.ELwispTools) != 1 {
			t.Fatalf("tools=%+v", got)
		}
		declaration := got.ELwispTools[0]
		if declaration.Name != "external" || declaration.CanonicalName != "elwisp_configured_external" || declaration.EventKey != queued.Event.EventKey {
			t.Fatalf("external declaration=%+v", declaration)
		}
		if got.ModelProvider != "provider-work" || got.Model != "model-work" {
			t.Fatalf("default model=%s/%s", got.ModelProvider, got.Model)
		}
	}
}

type backgroundDeclarationTool struct{ name string }

func (t backgroundDeclarationTool) Name() string { return t.name }
func (t backgroundDeclarationTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Risk: toolshare.RiskLow}
}
func (t backgroundDeclarationTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: t.name} }
func (value backgroundDeclarationTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		return &toolshare.Result{}, "", nil
	}), nil
}
