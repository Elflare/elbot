package preload

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestTagExpansionAndEmptyQueries(t *testing.T) {
	r := registry.NewRegistry(security.DefaultPermission())
	for _, name := range []string{"built_in", "configured", "unrelated"} {
		info := share.Info{}
		if name == "built_in" {
			info.Tags = []string{"group"}
		}
		if err := r.Register(availabilityTestTool{name: name, info: info}); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestPreloader(PreloadOptions{Registry: r, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"group": {Tools: []string{" configured ", "built_in", "configured"}, Prompt: "  instructions  "},
	}}})
	for _, tag := range []string{" GROUP ", "missing", "", "bad/tag"} {
		want := ""
		if tag == " GROUP " {
			want = "built_in,configured"
		}
		names, err := s.ToolNamesByTag(t.Context(), tag, share.ToolQuery{})
		if err != nil || strings.Join(names, ",") != want {
			t.Fatalf("tag=%q names=%v err=%v", tag, names, err)
		}
		prepared, err := s.PrepareTools(t.Context(), []string{tag})
		if err != nil || prepared.Accepted[0] != (want != "") || len(prepared.Update.Tools) != len(names) {
			t.Fatalf("tag=%q preload=%+v err=%v", tag, prepared, err)
		}
	}
	prompts, err := s.TagPrompts(t.Context(), []string{" GROUP ", "missing"})
	if err != nil || !reflect.DeepEqual(prompts, []share.TagPrompt{{Tag: " GROUP ", Prompt: "instructions"}}) {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}
	before := append([]string(nil), s.tags.cache.config.Tags["group"].Tools...)
	names, err := s.namesByTag(t.Context(), "group")
	if err != nil {
		t.Fatal(err)
	}
	names[0] = "changed"
	if !reflect.DeepEqual(before, s.tags.cache.config.Tags["group"].Tools) {
		t.Fatal("expanded names alias cached configuration")
	}
	// 无效标签无需读取配置；合法标签仍暴露配置错误。
	s = newTestPreloader(PreloadOptions{Registry: r, TagsPath: t.TempDir()})
	if names, err := s.ToolNamesByTag(t.Context(), "bad/tag", share.ToolQuery{}); err != nil || len(names) != 0 {
		t.Fatalf("invalid tag queried configuration or all tools: %v, %v", names, err)
	}
	if _, err := s.ToolNamesByTag(t.Context(), "group", share.ToolQuery{}); err == nil {
		t.Fatal("configuration read failure was hidden")
	}
}

func TestTagConfigFileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.toml")
	s := newToolTagConfigSource(path, config.ToolTagsConfig{})
	for _, step := range []struct {
		name, content, want    string
		write, remove, invalid bool
	}{
		{name: "missing"},
		{name: "created", write: true, content: "[tags.group]\ntools = ['first']", want: "first"},
		{name: "updated", write: true, content: "[tags.group]\ntools = ['second', 'third']", want: "second,third"},
		{name: "invalid", write: true, content: "[tags.", invalid: true},
		{name: "recovered", write: true, content: "[tags.group]\ntools = ['recovered']", want: "recovered"},
		{name: "deleted", remove: true},
		{name: "still_missing"},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.write {
				if err := os.WriteFile(path, []byte(step.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if step.remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before := s.cache
			cfg, err := s.load(t.Context())
			if step.invalid {
				if err == nil || !strings.Contains(err.Error(), "parse tool tags config") || !reflect.DeepEqual(before, s.cache) {
					t.Fatalf("invalid config changed cache or hid error: %v", err)
				}
				return
			}
			if err != nil || strings.Join(cfg.Tags["group"].Tools, ",") != step.want {
				t.Fatalf("config=%+v err=%v", cfg, err)
			}
		})
	}
}
