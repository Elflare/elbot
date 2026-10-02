package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func changeTOML(t *testing.T, path string, change func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	change(raw)
	data, err = toml.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func requireIssue(t *testing.T, report Report, path string, level config.Level, fragment string) config.Issue {
	t.Helper()
	for _, file := range report.Files {
		if file.Path == path {
			for _, issue := range file.Issues {
				if issue.Level == level && strings.Contains(issue.Message, fragment) {
					return issue
				}
			}
		}
	}
	t.Fatalf("missing %s %q at %s:\n%s", level, fragment, path, report.Text())
	return config.Issue{}
}

func requireErrorCount(t *testing.T, report Report, want int) {
	t.Helper()
	got := 0
	for _, file := range report.Files {
		for _, issue := range file.Issues {
			if issue.Level == config.LevelError {
				got++
			}
		}
	}
	if got != want {
		t.Fatalf("errors = %d, want %d:\n%s", got, want, report.Text())
	}
}

func TestMissingAssetsFollowRuntimeRequirements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level config.Level
	}{
		{"app.toml", config.LevelError},
		{"providers.toml", config.LevelError},
		{"SOUL.md", config.LevelError},
		{"state.toml", config.LevelHint},
		{"elnis.toml", config.LevelHint},
		{"tool_tags.toml", config.LevelHint},
		{"plugins/hooks.toml", config.LevelHint},
		{"plugins/.env", config.LevelHint},
		{"memories.toml", config.LevelHint},
		{"skills/agent/write_elbot_hook/SKILL.md", config.LevelHint},
		{"skills/agent/write_elbot_hook/ELBOT_SKILL.toml", config.LevelHint},
		{".env.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			// State may be absent when main config supplies the required models.
			changeTOML(t, filepath.Join(dir, "app.toml"), func(raw map[string]any) {
				raw["mode_models"] = map[string]any{
					"work": config.ModelSelection{Provider: "deepseek", Model: "work"},
					"chat": config.ModelSelection{Provider: "deepseek", Model: "chat"},
				}
			})
			path := filepath.Join(dir, tc.name)
			removeFile(t, path)
			report := check(t, service)
			if tc.level == "" {
				if report.Text() != "Everything is OK" {
					t.Fatal(report.Text())
				}
				return
			}
			requireIssue(t, report, path, tc.level, "文件缺失")
			wantErrors := 0
			if tc.level == config.LevelError {
				wantErrors = 1
			}
			requireErrorCount(t, report, wantErrors)
			if tc.level == config.LevelHint && !strings.HasPrefix(report.Text(), "未发现必要问题，有 1 项提示，涉及 1 个文件。") {
				t.Fatal(report.Text())
			}
			if tc.name == "app.toml" {
				for _, file := range report.Files {
					if file.Path == filepath.Join(dir, "providers.toml") {
						t.Fatal("guessed dependent path after missing main config")
					}
				}
			}
		})
	}
}

func TestUserNamedExamplesCanBeRemovedOrReplaced(t *testing.T) {
	dir, service := fixture(t)
	changeTOML(t, filepath.Join(dir, "app.toml"), func(raw map[string]any) {
		delete(raw["security"].(map[string]any), "superadmins")
		cli := raw["platform"].(map[string]any)["cli"].(map[string]any)
		delete(cli, "clients")
		delete(cli["server"].(map[string]any), "tokens")
	})
	changeTOML(t, filepath.Join(dir, "providers.toml"), func(raw map[string]any) {
		raw["providers"] = map[string]any{"custom": map[string]any{"models": []string{"mine"}}}
	})
	changeTOML(t, filepath.Join(dir, "state.toml"), func(raw map[string]any) {
		for _, selection := range raw["mode_models"].(map[string]any) {
			selection.(map[string]any)["provider"] = "custom"
			selection.(map[string]any)["model"] = "mine"
		}
	})
	changeTOML(t, filepath.Join(dir, "elnis.toml"), func(raw map[string]any) {
		delete(raw, "tokens")
		delete(raw, "elwisps")
	})
	writeFile(t, filepath.Join(dir, "plugins", "hooks.toml"), "")
	removeFile(t, filepath.Join(dir, ".env.example"))
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
}

func TestOptionalConfigFilesStillRejectInvalidTypes(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"state.toml", "[mode_models.chat]\nmodel = 123\n"},
		{"elnis.toml", "enabled = false\n[http]\nworkers = 'wrong'\n"},
		{"tool_tags.toml", "[tags.custom]\ntools = 123\n"},
		{"plugins/hooks.toml", "rules = 123\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			path := filepath.Join(dir, tc.name)
			writeFile(t, path, tc.content)
			report := check(t, service)
			requireErrorCount(t, report, 1)
			requireIssue(t, report, path, config.LevelError, "字段类型错误")
			for _, file := range report.Files {
				for _, issue := range file.Issues {
					if strings.Contains(issue.Message, "有效配置缺少") {
						t.Fatal("cascaded after a type failure:\n" + report.Text())
					}
				}
			}
		})
	}
}

func TestBuiltinTagHintsDoNotConstrainCustomTags(t *testing.T) {
	for _, tc := range []struct{ name, remove, want string }{
		{"custom tag", "", ""},
		{"missing builtin", "agent", "缺失节点：tags.agent"},
		{"missing prompt", "prompt", "缺失字段：tags.agent.prompt"},
		{"missing tools", "tools", "缺失字段：tags.agent.tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			path := filepath.Join(dir, "tool_tags.toml")
			changeTOML(t, path, func(raw map[string]any) {
				tags := raw["tags"].(map[string]any)
				tags["custom"] = map[string]any{"tools": []string{"shell"}}
				agent := tags["agent"].(map[string]any)
				agent["tools"] = []string{}
				agent["prompt"] = ""
				if tc.remove == "agent" {
					delete(tags, "agent")
				} else {
					delete(agent, tc.remove)
				}
			})
			report := check(t, service)
			requireErrorCount(t, report, 0)
			if tc.want == "" {
				if report.Text() != "Everything is OK" {
					t.Fatal(report.Text())
				}
			} else {
				requireIssue(t, report, path, config.LevelHint, tc.want)
				if len(report.Files) != 1 || len(report.Files[0].Issues) != 1 {
					t.Fatal(report.Text())
				}
			}
		})
	}
}

func TestEffectiveModelsFollowStateOverrides(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mainMode   string
		stateMode  string
		selection  map[string]any
		removeChat bool
		want       string
		wantErrors int
	}{
		{name: "state overrides main default", mainMode: "invalid", stateMode: "chat"},
		{name: "empty state default preserves main", mainMode: "chat", stateMode: ""},
		{name: "invalid effective default", stateMode: "invalid", want: "session.default_mode 必须", wantErrors: 1},
		{name: "state replaces whole selection", selection: map[string]any{"model": "state-model"}, want: "mode_models.work.provider", wantErrors: 1},
		{name: "missing model", selection: map[string]any{"provider": "deepseek"}, want: "mode_models.work.model", wantErrors: 1},
		{name: "unknown provider", selection: map[string]any{"provider": "only-in-main", "model": "m"}, want: "provider \"only-in-main\" 不存在", wantErrors: 1},
		{name: "chat from main", removeChat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			mainPath := filepath.Join(dir, "app.toml")
			statePath := filepath.Join(dir, "state.toml")
			changeTOML(t, mainPath, func(raw map[string]any) {
				raw["session"].(map[string]any)["default_mode"] = tc.mainMode
				raw["mode_models"] = map[string]any{
					"work": config.ModelSelection{Provider: "deepseek", Model: "main-work"},
					"chat": config.ModelSelection{Provider: "deepseek", Model: "main-chat"},
				}
				raw["providers"] = map[string]any{"only-in-main": map[string]any{"models": []string{"m"}}}
			})
			changeTOML(t, statePath, func(raw map[string]any) {
				raw["session"].(map[string]any)["default_mode"] = tc.stateMode
				models := raw["mode_models"].(map[string]any)
				if tc.selection != nil {
					models["work"] = tc.selection
				}
				if tc.removeChat {
					delete(models, "chat")
				}
			})
			report := check(t, service)
			requireErrorCount(t, report, tc.wantErrors)
			if tc.want != "" {
				requireIssue(t, report, statePath, config.LevelError, tc.want)
			} else if report.Text() != "Everything is OK" {
				t.Fatal(report.Text())
			}
			// Exercise the real loader too: provider references are checked by
			// the app, while default mode and selection completeness are here.
			_, err := config.Load(mainPath)
			if tc.wantErrors == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.wantErrors > 0 && tc.name != "unknown provider" && err == nil {
				t.Fatal("Doctor and config.Load disagree about a required setting")
			}
		})
	}
}

func TestRequiredAndOptionalModelSlots(t *testing.T) {
	dir, service := fixture(t)
	statePath := filepath.Join(dir, "state.toml")
	changeTOML(t, statePath, func(raw map[string]any) {
		models := raw["mode_models"].(map[string]any)
		delete(models["chat"].(map[string]any), "model")
		delete(models, "elwisp1")
	})
	report := check(t, service)
	requireErrorCount(t, report, 1)
	requireIssue(t, report, statePath, config.LevelError, "mode_models.chat.model")
	issue := requireIssue(t, report, statePath, config.LevelHint, "mode_models.elwisp1")
	if issue.Topic != config.TopicElnis || !strings.Contains(report.Text(), "Elnis 说明：") {
		t.Fatal(report.Text())
	}
	removeFile(t, statePath)
	report = check(t, service)
	requireErrorCount(t, report, 2)
	requireIssue(t, report, statePath, config.LevelHint, "文件缺失")
	if strings.Contains(report.Text(), "缺失可选模型槽") {
		t.Fatal("cascaded optional hints for a missing state file")
	}
}

func TestUnknownMainFieldsDoNotBlockSemanticChecks(t *testing.T) {
	dir, service := fixture(t)
	mainPath := filepath.Join(dir, "app.toml")
	statePath := filepath.Join(dir, "state.toml")
	changeTOML(t, mainPath, func(raw map[string]any) { raw["unknown_setting"] = true })
	changeTOML(t, statePath, func(raw map[string]any) {
		raw["mode_models"].(map[string]any)["chat"].(map[string]any)["provider"] = "missing"
	})
	report := check(t, service)
	requireErrorCount(t, report, 1)
	requireIssue(t, report, mainPath, config.LevelHint, "未知字段：unknown_setting")
	requireIssue(t, report, statePath, config.LevelError, "provider \"missing\" 不存在")
}

func TestElnisTokenDeclarationsAndConditionalLink(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		tokens     map[string]any
		wantErrors int
	}{
		{name: "disabled without tokens"},
		{name: "enabled without tokens", enabled: true, wantErrors: 1},
		{name: "custom token", enabled: true, tokens: map[string]any{"mine": map[string]any{"token_env": []string{"UNSET_CUSTOM_TOKEN"}}}},
		{name: "empty token env", enabled: true, tokens: map[string]any{"mine": map[string]any{"token_env": []string{"", " "}}}, wantErrors: 1},
		{name: "empty token name", enabled: true, tokens: map[string]any{" ": map[string]any{"token_env": []string{"TOKEN"}}}, wantErrors: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			path := filepath.Join(t.TempDir(), "custom hub.toml")
			if err := os.Rename(filepath.Join(dir, "elnis.toml"), path); err != nil {
				t.Fatal(err)
			}
			changeTOML(t, filepath.Join(dir, "app.toml"), func(raw map[string]any) {
				raw["config_files"].(map[string]any)["elnis"] = path
			})
			changeTOML(t, path, func(raw map[string]any) {
				raw["enabled"] = tc.enabled
				delete(raw, "tokens")
				if tc.tokens != nil {
					raw["tokens"] = tc.tokens
				}
			})
			// A directory at .env would fail secret resolution. Doctor must
			// inspect declarations only, without attempting to read secrets.
			if err := os.Mkdir(filepath.Join(dir, ".env"), 0o700); err != nil {
				t.Fatal(err)
			}
			report := check(t, service)
			requireErrorCount(t, report, tc.wantErrors)
			if tc.wantErrors > 0 {
				issue := requireIssue(t, report, path, config.LevelError, "token_env")
				if issue.Topic != config.TopicElnis || strings.Count(report.Text(), "Elnis 说明：") != 1 {
					t.Fatal(report.Text())
				}
			} else if report.Text() != "Everything is OK" {
				t.Fatal(report.Text())
			}
		})
	}
}

func TestElnisFileReplacesMainConfig(t *testing.T) {
	dir, service := fixture(t)
	mainPath := filepath.Join(dir, "app.toml")
	elnisPath := filepath.Join(dir, "elnis.toml")
	changeTOML(t, mainPath, func(raw map[string]any) {
		raw["elnis"] = map[string]any{"enabled": true}
	})
	// The bundled disabled Elnis file overrides main's enabled setting.
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	removeFile(t, elnisPath)
	report := check(t, service)
	requireErrorCount(t, report, 1)
	requireIssue(t, report, mainPath, config.LevelError, "Elnis 已启用")
	requireIssue(t, report, elnisPath, config.LevelHint, "文件缺失")
}

func TestElnisLinkTracksIssueTopic(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		change     func(map[string]any)
	}{
		{"path type", "app.toml", func(raw map[string]any) { raw["config_files"].(map[string]any)["elnis"] = 123 }},
		{"unknown inline setting", "app.toml", func(raw map[string]any) { raw["elnis"] = map[string]any{"typo": true} }},
		{"optional slot type", "state.toml", func(raw map[string]any) {
			raw["mode_models"].(map[string]any)["elwisp1"].(map[string]any)["model"] = 123
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			changeTOML(t, filepath.Join(dir, tc.file), tc.change)
			report := check(t, service)
			if strings.Count(report.Text(), "Elnis 说明：") != 1 {
				t.Fatal(report.Text())
			}
		})
	}
}
