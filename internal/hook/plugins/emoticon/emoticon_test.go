package emoticon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/output"
)

func TestModuleExtractsOutputsAndCleansContent(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"微笑", "开心"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	manager := hook.NewManager()
	module := Module{Config: Config{RootDir: root}}
	if err := module.RegisterHooks(manager); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	event, err := manager.Run(context.Background(), hook.Event{
		Point: hook.PointLLMResponseReceived,
		LLM: hook.LLMPayload{
			RawText: "你好[[微笑]]",
			Text:    "你好[[微笑]]，今天很开心[[开心]]",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.LLM.Text != "你好，今天很开心" {
		t.Fatalf("Text = %q", event.LLM.Text)
	}
	if event.LLM.RawText != "你好[[微笑]]" {
		t.Fatalf("RawText = %q, want original raw content", event.LLM.RawText)
	}
	if len(event.Outputs) != 2 {
		t.Fatalf("outputs len = %d, want 2", len(event.Outputs))
	}
	wantNames := []string{"微笑", "开心"}
	for i, want := range wantNames {
		if event.Outputs[i].Kind != output.KindEmoticon || event.Outputs[i].Name != want {
			t.Fatalf("output[%d] = %#v, want emoticon %q", i, event.Outputs[i], want)
		}
		if got := output.DeliveryTiming(event.Outputs[i]); got != output.DeliveryImmediate {
			t.Fatalf("output[%d] timing = %q, want %q", i, got, output.DeliveryImmediate)
		}
	}
}

func TestModuleLeavesUnknownTokenUnchanged(t *testing.T) {
	module := Module{Config: Config{RootDir: t.TempDir()}}
	event, err := module.rewriteLLMEmoticons(context.Background(), hook.Event{
		Point: hook.PointLLMResponseReceived,
		LLM:   hook.LLMPayload{Text: "这是 [[草稿]] 不是表情"},
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if event.LLM.Text != "这是 [[草稿]] 不是表情" {
		t.Fatalf("Text = %q, want unchanged", event.LLM.Text)
	}
	if len(event.Outputs) != 0 {
		t.Fatalf("outputs = %#v, want none", event.Outputs)
	}
}

func TestModuleOnlyConsumesKnownTokens(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "微笑"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	module := Module{Config: Config{RootDir: root}}
	event, err := module.rewriteLLMEmoticons(context.Background(), hook.Event{
		Point: hook.PointLLMResponseReceived,
		LLM:   hook.LLMPayload{Text: "你好 [[微笑]] [[草稿]]"},
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if event.LLM.Text != "你好  [[草稿]]" {
		t.Fatalf("Text = %q", event.LLM.Text)
	}
	if len(event.Outputs) != 1 || event.Outputs[0].Name != "微笑" {
		t.Fatalf("outputs = %#v", event.Outputs)
	}
}

func TestModuleUsesConfiguredTiming(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "微笑"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	module := Module{Config: Config{RootDir: root, Timing: output.DeliveryAfterAssistant}}
	event, err := module.rewriteLLMEmoticons(context.Background(), hook.Event{
		Point: hook.PointLLMResponseReceived,
		LLM:   hook.LLMPayload{Text: "你好 [[微笑]]"},
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if len(event.Outputs) != 1 {
		t.Fatalf("outputs = %#v", event.Outputs)
	}
	if got := output.DeliveryTiming(event.Outputs[0]); got != output.DeliveryAfterAssistant {
		t.Fatalf("timing = %q, want %q", got, output.DeliveryAfterAssistant)
	}
}

func TestLoadConfigRejectsUnsupportedTiming(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`timing = "later"`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, _, err := loadConfig(dir); err == nil || !strings.Contains(err.Error(), `unsupported timing "later"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadConfigInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("root_dir ="), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, _, err := loadConfig(dir); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestModulePicksImage(t *testing.T) {
	root := t.TempDir()
	kindDir := filepath.Join(root, "滑稽")
	if err := os.MkdirAll(kindDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(kindDir, "huaji.png")
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	module := Module{Config: Config{RootDir: root}}
	event, err := module.rewriteLLMEmoticons(context.Background(), hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "你好 [[滑稽]]"}})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if event.LLM.Text != "你好" {
		t.Fatalf("content = %q", event.LLM.Text)
	}
	if len(event.Outputs) != 1 {
		t.Fatalf("outputs = %#v", event.Outputs)
	}
	got := event.Outputs[0]
	if got.Kind != output.KindEmoticon || got.Name != "滑稽" || got.Source.Path != path {
		t.Fatalf("output = %#v", got)
	}
	if timing := output.DeliveryTiming(got); timing != output.DeliveryImmediate {
		t.Fatalf("timing = %q, want %q", timing, output.DeliveryImmediate)
	}
}

func TestModuleIgnoresOtherPoints(t *testing.T) {
	module := Module{}
	event, err := module.rewriteLLMEmoticons(context.Background(), hook.Event{
		Point:   hook.PointAgentOutputPrepared,
		Message: hook.MessagePayload{Segments: llm.TextSegments("你好[[微笑]]")},
		LLM:     hook.LLMPayload{Text: "你好[[微笑]]"},
	})
	if err != nil {
		t.Fatalf("rewriteLLMEmoticons: %v", err)
	}
	if event.LLM.Text != "你好[[微笑]]" {
		t.Fatalf("Text = %q, want unchanged", event.LLM.Text)
	}
	if len(event.Outputs) != 0 {
		t.Fatalf("outputs = %#v, want none", event.Outputs)
	}
}
