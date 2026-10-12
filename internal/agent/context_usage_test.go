package agent

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
)

func TestContextRuntimeEvaluatesThresholdWithoutStickyState(t *testing.T) {
	models := newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"p": &fakeLLM{}}, Providers: map[string]config.ProviderConfig{"p": {}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}},
		Metadata:   config.ModelMetadataConfig{DefaultContextWindow: 100},
	})
	runtime := contextmgr.New(contextmgr.Options{Models: models})
	runtime.Configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
	)
	selection := models.ResolveMode("work")

	if runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: func() *int64 { n := int64(79); return &n }()}, selection) {
		t.Fatal("usage below threshold reached threshold")
	}
	if !runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: func() *int64 { n := int64(80); return &n }()}, selection) {
		t.Fatal("usage at threshold did not reach threshold")
	}

	runtime.Configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
	)
	if runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: func() *int64 { n := int64(100); return &n }()}, selection) {
		t.Fatal("disabled compact reached threshold")
	}
}

func TestContextRuntimeStatus(t *testing.T) {
	models := newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"p": &fakeLLM{}}, Providers: map[string]config.ProviderConfig{"p": {}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}},
		Metadata:   config.ModelMetadataConfig{DefaultContextWindow: 100},
	})
	runtime := contextmgr.New(contextmgr.Options{Models: models})
	runtime.Configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
	)
	fallback := config.ModelSelection{Provider: "p", Model: "m"}
	status := runtime.Status(context.Background(), &llm.Usage{TotalTokens: func() *int64 { n := int64(80); return &n }(), CacheHitTokens: func() *int64 { n := int64(10); return &n }()}, fallback)
	for _, want := range []string{"tokens：80（命中：10）", "context window: 100", "context usage: 80.0%", "compact status: will compact before next request"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	runtime.Configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
	)
	status = runtime.Status(context.Background(), &llm.Usage{TotalTokens: func() *int64 { n := int64(100); return &n }()}, fallback)
	if !strings.Contains(status, "compact status: disabled") {
		t.Fatalf("disabled status = %q", status)
	}
}
