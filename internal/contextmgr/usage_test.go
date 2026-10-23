package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
)

func TestStatusCompactLayout(t *testing.T) {
	selection := config.ModelSelection{Provider: "test", Model: "model"}
	models, err := modelmgr.New(modelmgr.Options{
		Clients:    map[string]llm.Client{"test": struct{ llm.Client }{}},
		Providers:  map[string]config.ProviderConfig{"test": {Models: []string{"model"}}},
		ModeModels: map[string]config.ModelSelection{"work": selection},
		Metadata:   config.ModelMetadataConfig{DefaultContextWindow: 128000},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	zero, normal, near, threshold, negative := int64(0), int64(54400), int64(96000), int64(102400), int64(-1)
	for _, tc := range []struct {
		name            string
		usage           *llm.Usage
		disabled        bool
		percent, status string
	}{
		{name: "normal", usage: &llm.Usage{TotalTokens: &normal}, percent: "42.5%", status: "ok"},
		{name: "zero", usage: &llm.Usage{TotalTokens: &zero}, percent: "0.0%", status: "ok"},
		{name: "near", usage: &llm.Usage{TotalTokens: &near}, percent: "75.0%", status: "near threshold"},
		{name: "threshold", usage: &llm.Usage{TotalTokens: &threshold}, percent: "80.0%", status: "will compact before next request"},
		{name: "disabled", usage: &llm.Usage{TotalTokens: &threshold}, disabled: true, percent: "80.0%", status: "disabled"},
		{name: "nil usage", percent: "unknown", status: "unknown"},
		{name: "missing total", usage: &llm.Usage{}, percent: "unknown", status: "unknown"},
		{name: "negative total", usage: &llm.Usage{TotalTokens: &negative}, percent: "unknown", status: "unknown"},
		{name: "unknown disabled", disabled: true, percent: "unknown", status: "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Options{Models: models, Config: config.ContextConfig{CompactEnabled: !tc.disabled, CompactTriggerRatio: 0.8}})
			got := svc.Status(context.Background(), tc.usage, selection)
			want := fmt.Sprintf("  context window: 128000 (%s)\n  compact threshold: 80%%, status: %s\n", tc.percent, tc.status)
			if !strings.HasSuffix(got, want) || strings.Count(got, "\n") != 3 {
				t.Fatalf("status = %q, want token line followed by %q", got, want)
			}
		})
	}
}

func TestUsageStateAndFormatTokens(t *testing.T) {
	total, cache := int64(90), int64(10)
	state := UsageState{Usage: &llm.Usage{TotalTokens: &total, CacheHitTokens: &cache}, ContextWindow: 100, TriggerRatio: 0.8}
	if !state.ReachedThreshold() {
		t.Fatal("expected threshold reached")
	}
	if got := FormatTokens(state.Usage); got != "tokens：90（命中：10）" {
		t.Fatalf("tokens = %q", got)
	}
	if got := FormatTokens(nil); got != "tokens：unknown（命中：unknown）" {
		t.Fatalf("unknown tokens = %q", got)
	}
}

func TestUsageUnknownAndZero(t *testing.T) {
	zero, cache, negative := int64(0), int64(3), int64(-1)
	for _, tc := range []struct {
		usage *llm.Usage
		known bool
		text  string
	}{
		{nil, false, "tokens：unknown（命中：unknown）"},
		{&llm.Usage{}, false, "tokens：unknown（命中：unknown）"},
		{&llm.Usage{CacheHitTokens: &cache}, false, "tokens：unknown（命中：3）"},
		{&llm.Usage{TotalTokens: &zero}, true, "tokens：0（命中：unknown）"},
		{&llm.Usage{TotalTokens: &zero, CacheHitTokens: &zero}, true, "tokens：0（命中：0）"},
		{&llm.Usage{TotalTokens: &negative, CacheHitTokens: &cache}, false, "tokens：unknown（命中：3）"},
	} {
		state := UsageState{Usage: tc.usage, ContextWindow: 100, TriggerRatio: 0.8}
		if state.TokensKnown() != tc.known || state.ReachedThreshold() || state.UsageRatio() != 0 {
			t.Fatalf("incorrect known/threshold/ratio for %#v", tc.usage)
		}
		if got := FormatTokens(tc.usage); got != tc.text {
			t.Errorf("got %q, want %q", got, tc.text)
		}
	}
}
