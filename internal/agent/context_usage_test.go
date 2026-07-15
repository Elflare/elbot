package agent

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

func TestContextRuntimeEvaluatesThresholdWithoutStickyState(t *testing.T) {
	runtime := newContextRuntimeState(nil, nil, nil, nil)
	runtime.configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
		config.ModelSelection{},
		nil,
	)
	selection := config.ModelSelection{Provider: "p", Model: "m"}

	if runtime.reachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 79}, selection) {
		t.Fatal("usage below threshold reached threshold")
	}
	if !runtime.reachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 80}, selection) {
		t.Fatal("usage at threshold did not reach threshold")
	}

	runtime.configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
		config.ModelSelection{},
		nil,
	)
	if runtime.reachedCompactThreshold(context.Background(), &llm.Usage{TotalTokens: 100}, selection) {
		t.Fatal("disabled compact reached threshold")
	}
}

func TestContextRuntimeStatusAndCompactModelFallback(t *testing.T) {
	runtime := newContextRuntimeState(nil, nil, nil, nil)
	runtime.configure(
		config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
		config.ModelSelection{},
		nil,
	)
	fallback := config.ModelSelection{Provider: "chat", Model: "main"}
	if got := runtime.compactSelection(fallback); got != fallback {
		t.Fatalf("compact fallback = %#v", got)
	}
	override := config.ModelSelection{Provider: "compact", Model: "small"}
	runtime.setCompactModel(override)
	if got := runtime.compactSelection(fallback); got != override {
		t.Fatalf("compact override = %#v", got)
	}

	status := runtime.status(context.Background(), "s", &llm.Usage{TotalTokens: 80, CacheHitTokens: 10}, fallback)
	for _, want := range []string{"tokens：80（命中：10）", "context window: 100", "context usage: 80.0%", "compact status: will compact before next request"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	runtime.configure(
		config.ContextConfig{CompactEnabled: false, CompactTriggerRatio: 0.8},
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		nil,
		config.ModelSelection{},
		nil,
	)
	status = runtime.status(context.Background(), "s", &llm.Usage{TotalTokens: 100}, fallback)
	if !strings.Contains(status, "compact status: disabled") {
		t.Fatalf("disabled status = %q", status)
	}
}
