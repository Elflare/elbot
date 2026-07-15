package contextmgr

import (
	"context"
	"sync"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type metadataLLM struct {
	llm.LLM
	metadata []llm.ModelMetadata
}

func TestWindowResolverConcurrentCacheAccess(t *testing.T) {
	resolver := NewWindowResolver(
		config.ModelMetadataConfig{DefaultContextWindow: 100},
		map[string]config.ProviderConfig{"p": {ModelConfigs: map[string]config.ModelConfig{"manual": {ContextWindow: 200}}}},
		func(string) llm.LLM {
			return metadataLLM{metadata: []llm.ModelMetadata{{ID: "api", ContextWindow: 300}}}
		},
	)
	want := map[string]int{"api": 300, "manual": 200, "unknown": 100}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		for model, window := range want {
			model, window := model, window
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := resolver.Resolve(context.Background(), "p", model); got != window {
					t.Errorf("window for %s = %d, want %d", model, got, window)
				}
			}()
		}
	}
	wg.Wait()
}

func (m metadataLLM) ListModelMetadata(context.Context) ([]llm.ModelMetadata, error) {
	return m.metadata, nil
}

func TestWindowResolverPriority(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"p": {ModelConfigs: map[string]config.ModelConfig{
			"manual":   {ContextWindow: 16000},
			"fallback": {ContextWindow: 12000},
		}},
	}
	resolver := NewWindowResolver(
		config.ModelMetadataConfig{DefaultContextWindow: 8192},
		providers,
		func(provider string) llm.LLM {
			return metadataLLM{metadata: []llm.ModelMetadata{{ID: "api", ContextWindow: 32000}}}
		},
	)

	if got := resolver.Resolve(context.Background(), "p", "api"); got != 32000 {
		t.Fatalf("api window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "manual"); got != 16000 {
		t.Fatalf("manual provider/model window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "fallback"); got != 12000 {
		t.Fatalf("fallback provider/model window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "unknown"); got != 8192 {
		t.Fatalf("default window = %d", got)
	}
	if got := (&WindowResolver{}).Resolve(context.Background(), "p", "unknown"); got != config.DefaultContextWindow {
		t.Fatalf("built-in default window = %d", got)
	}
}
