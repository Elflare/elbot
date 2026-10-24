package preload

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/logging"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

type preloadContentTool struct {
	availabilityTestTool
	err   error
	calls int
}

func (t *preloadContentTool) DiscoveryContent(context.Context) (string, bool, error) {
	t.calls++
	return "Use the tool with its task instructions.", false, t.err
}

func TestBackgroundPreloadIncludesToolContent(t *testing.T) {
	registry := tool.NewRegistry()
	candidate := &preloadContentTool{availabilityTestTool: availabilityTestTool{name: "task_tool"}}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	preloader := NewPreloadService(PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"tasks": {Tools: []string{"task_tool"}}}}})
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Background: true})
	prepared, err := preloader.PrepareBackground(ctx, "session", []string{"task_tool", "tasks", "task_tool"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Update.Tools) != 1 || !strings.Contains(prepared.SkillPrompt, "Use the tool with its task instructions.") || candidate.calls != 1 {
		t.Fatalf("preload=%+v content loads=%d", prepared, candidate.calls)
	}
	if strings.Join(prepared.Update.Tags, ",") != "tasks" {
		t.Fatalf("tags=%v", prepared.Update.Tags)
	}
	prepared, err = preloader.PrepareBackground(ctx, "session", []string{"tasks"}, []string{})
	if err != nil || len(prepared.Update.Tools) != 0 || len(prepared.Update.Tags) != 0 || prepared.SkillPrompt != "" || candidate.calls != 1 {
		t.Fatalf("unauthorized preload=%+v content loads=%d err=%v", prepared, candidate.calls, err)
	}
	candidate.err = errors.New("content failed")
	prepared, err = preloader.PrepareBackground(ctx, "session", []string{"task_tool"}, nil)
	if !errors.Is(err, candidate.err) || len(prepared.Update.Tools) != 0 || prepared.SkillPrompt != "" {
		t.Fatalf("failed preload=%+v err=%v", prepared, err)
	}
	candidate.err = nil
	if err := registry.Register(skill.NewDescriptor(skill.Record{Name: "broken", DetailPath: filepath.Join(t.TempDir(), "missing.md")})); err != nil {
		t.Fatal(err)
	}
	prepared, err = preloader.PrepareBackground(ctx, "session", []string{"tasks", "broken"}, nil)
	if err == nil || len(prepared.Update.Tools) != 0 || len(prepared.Update.Tags) != 0 || prepared.SkillPrompt != "" {
		t.Fatalf("skill failure leaked tool preparation: %+v err=%v", prepared, err)
	}
}

func TestPreloadWrapperSkipAuditUsesEventName(t *testing.T) {
	for _, background := range []bool{false, true} {
		eventName, targetKey := "skill_wrapper_preload_skipped", "tool"
		if background {
			eventName, targetKey = "background_preload_skipped", "name"
		}
		t.Run(eventName, func(t *testing.T) {
			manager, err := logging.NewManager("error", filepath.Join(t.TempDir(), "sessions.db"), 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			preloader := NewPreloadService(PreloadOptions{Registry: tool.NewRegistry()})
			if got := preloader.preloadWrapper(context.Background(), "session", "missing", background); len(got) != 0 {
				t.Fatalf("unexpected tools: %+v", got)
			}
			if err := manager.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(manager.LogDir(), "audit-*.log"))
			if err != nil || len(files) != 1 {
				t.Fatalf("files=%v err=%v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]string
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["event"] != eventName || fields["session_id"] != "session" || !strings.Contains(fields["content"], targetKey+"=missing") || !strings.Contains(fields["content"], "reason=not_found_or_not_allowed") || fields["result_status"] != "skipped" {
				t.Fatalf("lost skip facts: %+v", fields)
			}
		})
	}
}
