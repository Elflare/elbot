package agent

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
)

func TestContextRuntimeEvaluatesThresholdWithoutStickyState(t *testing.T) {
	runtime := contextmgr.New(contextmgr.Options{})
	runtime.Configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
	)
	selection := config.ModelSelection{Provider: "p", Model: "m"}

	if runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 79}, selection) {
		t.Fatal("usage below threshold reached threshold")
	}
	if !runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 80}, selection) {
		t.Fatal("usage at threshold did not reach threshold")
	}

	runtime.Configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
	)
	if runtime.ReachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 100}, selection) {
		t.Fatal("disabled compact reached threshold")
	}
}

func TestContextRuntimeStatus(t *testing.T) {
	runtime := contextmgr.New(contextmgr.Options{})
	runtime.Configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
	)
	fallback := config.ModelSelection{Provider: "chat", Model: "main"}
	status := runtime.Status(context.Background(), &llm.Usage{TotalTokens: 80, CacheHitTokens: 10}, fallback)
	for _, want := range []string{"tokens：80（命中：10）", "context window: 100", "context usage: 80.0%", "compact status: will compact before next request"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	runtime.Configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
	)
	status = runtime.Status(context.Background(), &llm.Usage{TotalTokens: 100}, fallback)
	if !strings.Contains(status, "compact status: disabled") {
		t.Fatalf("disabled status = %q", status)
	}
}
