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
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{
		Providers:     map[string]config.ProviderConfig{"test": {Models: []string{"model"}}},
		ModeModels:    map[string]config.ModelSelection{"work": selection},
		ModelMetadata: config.ModelMetadataConfig{DefaultContextWindow: 128000},
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
		{name: "near", usage: &llm.Usage{TotalTokens: &near}, percent: "75.0%", status: "near"},
		{name: "threshold", usage: &llm.Usage{TotalTokens: &threshold}, percent: "80.0%", status: "next request"},
		{name: "disabled", usage: &llm.Usage{TotalTokens: &threshold}, disabled: true, percent: "80.0%", status: "off"},
		{name: "nil usage", percent: "unk", status: "unk"},
		{name: "missing total", usage: &llm.Usage{}, percent: "unk", status: "unk"},
		{name: "negative total", usage: &llm.Usage{TotalTokens: &negative}, percent: "unk", status: "unk"},
		{name: "unknown disabled", disabled: true, percent: "unk", status: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Options{Models: models, Config: config.ContextConfig{CompactEnabled: !tc.disabled, CompactTriggerRatio: 0.8}})
			got := svc.Status(context.Background(), tc.usage, selection)
			want := "ctx.window=128000 · ratio=unk\n"
			if tc.percent != "unk" {
				want = fmt.Sprintf("ctx=%d/128000 (%s)\n", *tc.usage.TotalTokens, tc.percent)
			}
			want += fmt.Sprintf("compact=80%% · %s\n", tc.status)
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
	if got := FormatTokens(state.Usage); got != "tokens=90 · cache=10" {
		t.Fatalf("tokens = %q", got)
	}
	if got := FormatTokens(nil); got != "tokens=unk · cache=unk" {
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
		{nil, false, "tokens=unk · cache=unk"},
		{&llm.Usage{}, false, "tokens=unk · cache=unk"},
		{&llm.Usage{CacheHitTokens: &cache}, false, "tokens=unk · cache=3"},
		{&llm.Usage{TotalTokens: &zero}, true, "tokens=0 · cache=unk"},
		{&llm.Usage{TotalTokens: &zero, CacheHitTokens: &zero}, true, "tokens=0 · cache=0"},
		{&llm.Usage{TotalTokens: &negative, CacheHitTokens: &cache}, false, "tokens=unk · cache=3"},
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

func TestFormatTokensCachePercentage(t *testing.T) {
	zero, negative, input, cache, total := int64(0), int64(-1), int64(30), int64(10), int64(90)
	for _, tc := range []struct {
		name                string
		input, cache, total *int64
		want                string
	}{
		{"input denominator and rounding", &input, &cache, &total, "tokens=90 · cache=10 (33.3%)"},
		{"zero cache", &input, &zero, &total, "tokens=90 · cache=0 (0.0%)"},
		{"full cache", &input, &input, &total, "tokens=90 · cache=30 (100.0%)"},
		{"unknown input", nil, &cache, &total, "tokens=90 · cache=10"},
		{"zero input", &zero, &zero, &total, "tokens=90 · cache=0"},
		{"negative input", &negative, &cache, &total, "tokens=90 · cache=10"},
		{"unknown cache", &input, nil, &total, "tokens=90 · cache=unk"},
		{"negative cache", &input, &negative, &total, "tokens=90 · cache=unk"},
		{"unknown total", &input, &cache, nil, "tokens=unk · cache=10 (33.3%)"},
		{"negative total", &input, &cache, &negative, "tokens=unk · cache=10 (33.3%)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatTokens(&llm.Usage{PromptTokens: tc.input, CacheHitTokens: tc.cache, TotalTokens: tc.total})
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
