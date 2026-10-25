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
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

type preloadContentTool struct {
	availabilityTestTool
	err       error
	calls     int
	onContent func()
}

type availabilityTestTool struct {
	name     string
	info     share.Info
	onSchema func()
}

func (t availabilityTestTool) Name() string { return t.name }
func (t availabilityTestTool) Info() share.Info {
	info := t.info
	info.Name = t.name
	info.Risk = share.RiskLow
	return info
}
func (t availabilityTestTool) Schema() llm.ToolSchema {
	if t.onSchema != nil {
		t.onSchema()
	}
	return llm.ToolSchema{Name: t.name, Description: t.info.Description, Parameters: map[string]any{"type": "object"}}
}
func (availabilityTestTool) Call(context.Context, share.CallRequest) (*share.Result, string, error) {
	return &share.Result{}, "", nil
}

type preloadSkill struct {
	availabilityTestTool
	err      error
	calls    int
	activate []string
}

func (s *preloadSkill) Detail() string          { return "skill instructions" }
func (s *preloadSkill) ActivateTools() []string { return s.activate }
func (s *preloadSkill) LoadDetail(context.Context) (share.DetailBlock, error) {
	s.calls++
	return share.DetailBlock{Content: s.Detail()}, s.err
}

func (t *preloadContentTool) DiscoveryContent(context.Context) (string, bool, error) {
	t.calls++
	if t.onContent != nil {
		t.onContent()
	}
	return "Use the tool with its task instructions.", false, t.err
}

func TestBackgroundPreloadIncludesToolContent(t *testing.T) {
	registry := registry.NewRegistry(security.DefaultPermission())
	candidate := &preloadContentTool{availabilityTestTool: availabilityTestTool{name: "task_tool"}}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	preloader := NewPreloadService(PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"tasks": {Tools: []string{"task_tool"}}}}})
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	ctx = contextinfo.WithBackgroundKind(ctx, "")
	prepared, err := preloader.PrepareBackground(ctx, share.BackgroundPreloadRequest{SessionID: "session", Names: []string{"task_tool", "tasks", "task_tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Update.Tools) != 1 || !strings.Contains(prepared.SkillPrompt, "Use the tool with its task instructions.") || candidate.calls != 1 {
		t.Fatalf("preload=%+v content loads=%d", prepared, candidate.calls)
	}
	if strings.Join(prepared.Update.Tags, ",") != "tasks" {
		t.Fatalf("tags=%v", prepared.Update.Tags)
	}
	prepared, err = preloader.PrepareBackground(ctx, share.BackgroundPreloadRequest{SessionID: "session", Names: []string{"tasks"}, AllowedToolNames: []string{}})
	if err != nil || len(prepared.Update.Tools) != 0 || len(prepared.Update.Tags) != 0 || prepared.SkillPrompt != "" || candidate.calls != 1 {
		t.Fatalf("unauthorized preload=%+v content loads=%d err=%v", prepared, candidate.calls, err)
	}
	candidate.err = errors.New("content failed")
	prepared, err = preloader.PrepareBackground(ctx, share.BackgroundPreloadRequest{SessionID: "session", Names: []string{"task_tool"}})
	if !errors.Is(err, candidate.err) || len(prepared.Update.Tools) != 0 || prepared.SkillPrompt != "" {
		t.Fatalf("failed preload=%+v err=%v", prepared, err)
	}
	candidate.err = nil
	if err := registry.Register(&preloadSkill{availabilityTestTool: availabilityTestTool{name: "broken"}, err: errors.New("detail failed")}); err != nil {
		t.Fatal(err)
	}
	prepared, err = preloader.PrepareBackground(ctx, share.BackgroundPreloadRequest{SessionID: "session", Names: []string{"tasks", "broken"}})
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
			preloader := NewPreloadService(PreloadOptions{Registry: registry.NewRegistry(security.DefaultPermission())})
			if got, err := preloader.preloadWrapper(context.Background(), "session", "missing", background); len(got) != 0 || err != nil {
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

func TestBackgroundPreloadLocalAndELwisp(t *testing.T) {
	r := registry.NewRegistry(security.DefaultPermission())
	wrapper := availabilityTestTool{name: "wrapper"}
	skill := &preloadSkill{availabilityTestTool: availabilityTestTool{name: "guide"}, activate: []string{"wrapper", "wrapper"}}
	for _, tool := range []share.Tool{wrapper, skill} {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	s := NewPreloadService(PreloadOptions{Registry: r})
	ctx := contextinfo.WithBackgroundKind(t.Context(), "")
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	req := share.BackgroundPreloadRequest{Names: []string{"guide", "guide"}, ELwispTools: []share.ELwispToolDeclaration{
		{Name: "remote", CanonicalName: "source_remote", EventKey: "event", Endpoint: "https://example.invalid", TimeoutSeconds: 9, Schema: schema},
		{Name: "front", CanonicalName: "source_front", ForegroundOnly: true},
	}}
	got, err := s.PrepareBackground(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Update.Tools) != 2 || len(got.Skills) != 1 || skill.calls != 1 || !strings.Contains(got.SkillPrompt, "skill instructions") {
		t.Fatalf("got=%+v loads=%d", got, skill.calls)
	}
	remote := got.Update.Tools[1]
	if remote.Name != "remote" || remote.CanonicalName != "source_remote" || remote.Schema.Name != "source_remote" || remote.EventKey != "event" || remote.TimeoutSeconds != 9 || remote.Source != share.SourceKindELwisp {
		t.Fatalf("remote=%+v", remote)
	}
	schema["properties"].(map[string]any)["x"].(map[string]any)["type"] = "number"
	if remote.Schema.Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] != "string" {
		t.Fatal("remote schema aliases request")
	}
	req.AllowedToolNames = []string{}
	got, err = s.PrepareBackground(ctx, req)
	if err != nil || len(got.Update.Tools) != 1 || len(got.Skills) != 0 || skill.calls != 1 {
		t.Fatalf("local allowlist affected remote: %+v err=%v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	got, err = s.PrepareBackground(canceled, req)
	if !errors.Is(err, context.Canceled) || len(got.Update.Tools) != 0 {
		t.Fatalf("canceled preload=%+v err=%v", got, err)
	}
}

func TestSelectionsExpandBeforeRuntimeQualification(t *testing.T) {
	r := registry.NewRegistry(security.DefaultPermission())
	for _, tool := range []share.Tool{
		availabilityTestTool{name: "normal"},
		availabilityTestTool{name: "admin", info: share.Info{SuperadminOnly: true}},
		availabilityTestTool{name: "hidden", info: share.Info{Hidden: true}},
		availabilityTestTool{name: "api", info: share.Info{APITypes: []llm.APIType{"restricted"}}},
		&preloadSkill{availabilityTestTool: availabilityTestTool{name: "guide"}},
	} {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	s := NewPreloadService(PreloadOptions{Registry: r, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"group": {Tools: []string{"normal", "admin", "hidden", "api", "guide"}},
	}}})
	selections, err := s.BackgroundSelections(t.Context(), []string{"group"})
	if err != nil || len(selections) != 1 || strings.Join(selections[0].Names, ",") != "admin,api,guide,hidden,normal" {
		t.Fatalf("selections=%+v err=%v", selections, err)
	}
	for _, tc := range []struct {
		query share.ToolQuery
		want  string
	}{
		{share.ToolQuery{Kind: "tool"}, "normal"},
		{share.ToolQuery{Kind: "skill"}, "guide"},
		{share.ToolQuery{Names: []string{}}, ""},
		{share.ToolQuery{Names: []string{"normal", "guide"}}, "guide,normal"},
	} {
		got, err := s.ToolNamesByTag(t.Context(), "group", tc.query)
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Fatalf("query=%+v got=%v err=%v", tc.query, got, err)
		}
	}
}

func TestPreloadKeepsSelectedObjectAfterReplacement(t *testing.T) {
	r := registry.NewRegistry(security.DefaultPermission())
	replacement := &preloadContentTool{availabilityTestTool: availabilityTestTool{name: "dynamic", info: share.Info{Source: share.Source("test"), Description: "new"}}}
	original := &preloadContentTool{availabilityTestTool: availabilityTestTool{name: "dynamic", info: share.Info{Source: share.Source("test"), Description: "old"}}}
	original.onSchema = func() {
		if err := r.ReplaceSources([]share.Source{"test"}, []share.Tool{replacement}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(original); err != nil {
		t.Fatal(err)
	}
	got, err := NewPreloadService(PreloadOptions{Registry: r}).PrepareTools(t.Context(), []string{"dynamic"})
	if err != nil || len(got.Update.Tools) != 1 || got.Update.Tools[0].Schema.Description != "old" || original.calls != 1 || replacement.calls != 0 {
		t.Fatalf("got=%+v original=%d replacement=%d err=%v", got, original.calls, replacement.calls, err)
	}
}

func TestPrepareToolsCancellationDuringContent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	candidate := &preloadContentTool{availabilityTestTool: availabilityTestTool{name: "cancel"}, onContent: cancel}
	r := registry.NewRegistry(security.DefaultPermission())
	if err := r.Register(candidate); err != nil {
		t.Fatal(err)
	}
	got, err := NewPreloadService(PreloadOptions{Registry: r}).PrepareTools(ctx, []string{"cancel"})
	if !errors.Is(err, context.Canceled) || len(got.Update.Tools) != 0 || got.Content != "" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
