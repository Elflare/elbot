package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/hook/rules"
	"elbot/internal/platform/builtin"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func editFile(t *testing.T, path, old, next string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	writeFile(t, path, strings.Replace(string(data), old, next, 1))
}

func fixture(t *testing.T) (string, *Service) {
	t.Helper()
	dir := t.TempDir()
	for _, asset := range config.DefaultAssets() {
		writeFile(t, filepath.Join(dir, asset.Path), asset.Content)
	}
	service, err := New(filepath.Join(dir, "app.toml"), testInspector())
	if err != nil {
		t.Fatal(err)
	}
	return dir, service
}

func check(t *testing.T, service *Service) Report {
	t.Helper()
	report, err := service.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func diskSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v/%d", info.Mode(), info.ModTime().UnixNano())
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += "/" + string(data)
		}
		out[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHealthyAssetsAndReadOnlyCheck(t *testing.T) {
	dir, service := fixture(t)
	before := diskSnapshot(t, dir)
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	if after := diskSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("doctor modified assets or timestamps")
	}
}

func TestCustomPathsAndOptionalFields(t *testing.T) {
	dir, _ := fixture(t)
	mainPath := filepath.Join(dir, "custom config.toml")
	if err := os.Rename(filepath.Join(dir, "app.toml"), mainPath); err != nil {
		t.Fatal(err)
	}
	providers := filepath.Join(t.TempDir(), "my providers.toml")
	if err := os.Rename(filepath.Join(dir, "providers.toml"), providers); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "nested", "state.toml")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "state.toml"), state); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "SOUL.md"), filepath.Join(dir, "custom soul.md")); err != nil {
		t.Fatal(err)
	}
	editFile(t, mainPath, `providers = "providers.toml"`, "providers = "+strconv.Quote(providers))
	editFile(t, mainPath, `state = "state.toml"`, `state = "nested/state.toml"`)
	editFile(t, mainPath, `path = "SOUL.md"`, `path = "custom soul.md"`)
	editFile(t, mainPath, "# Main application config.", "# Custom main configuration.")
	service, err := New(mainPath, testInspector())
	if err != nil {
		t.Fatal(err)
	}
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	editFile(t, providers, "default_context_window = 256000\n", "")
	report := check(t, service)
	if len(report.Files) != 1 || report.Files[0].Path != providers || report.ConfigPath != mainPath {
		t.Fatalf("wrong resolved paths: %#v", report)
	}
}

func TestMissingUnknownAndSkillDifferencesAreGrouped(t *testing.T) {
	dir, service := fixture(t)
	main := filepath.Join(dir, "app.toml")
	editFile(t, main, "llm_image_max_length = 4096\n", "")
	editFile(t, main, `log_level = "info"`, `log_levle = "debug"`)
	skill := filepath.Join(dir, "skills", "agent", "agent_skill_creator", "SKILL.md")
	writeFile(t, skill, "# customized skill\n")
	before := diskSnapshot(t, dir)
	report := check(t, service)
	if len(report.Files) != 2 || report.Files[0].Path != main || report.Files[1].Path != skill {
		t.Fatalf("files = %#v", report.Files)
	}
	for _, want := range []string{"缺失字段：media.llm_image_max_length", "未知字段：runtime.log_levle", "缺失字段：runtime.log_level", "内容与当前内置版本不同，需要查看差异。"} {
		if !strings.Contains(report.Text(), want) {
			t.Fatalf("missing %q in %s", want, report.Text())
		}
	}
	if again := check(t, service); !reflect.DeepEqual(report, again) {
		t.Fatal("diagnosis order is not stable")
	}
	if !reflect.DeepEqual(before, diskSnapshot(t, dir)) {
		t.Fatal("doctor changed files with problems")
	}
}

func TestMainUnknownFieldsUseRealSchema(t *testing.T) {
	dir, service := fixture(t)
	main := filepath.Join(dir, "app.toml")
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, main, string(data)+`
[providers.custom]
api_key_env = "CUSTOM_KEY"
extra_payload = { arbitrary_vendor_option = true }
[providers.custom.model_configs."model.v1"]
context_window = 50000
extra_payload = { another_option = "value" }
[platform.cli.clients.workstation]
id = "remote"
url = "ws://example.invalid/cli/v1/ws"
token_env = ["CUSTOM_TOKEN"]
[platform.telegram]
enabled = false
api_base_url = "https://example.invalid"
reconnect_interval_seconds = 10
`)
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	editFile(t, main, `api_base_url = "https://example.invalid"`, `api_base_urll = "https://example.invalid"`)
	editFile(t, main, "[platform.cli.clients.workstation]", "[platform.cli.clients.workstation]\ntoken_en = []")
	editFile(t, main, "\n[platform.telegram]\n", "\n[platform.typo]\nenabled = false\n[platform.telegram]\n")
	report := check(t, service)
	for _, want := range []string{"未知字段：platform.telegram.api_base_urll", "未知字段：platform.cli.clients.workstation.token_en", "未知字段：platform.typo"} {
		if !strings.Contains(report.Text(), want) {
			t.Fatalf("missing %q in %s", want, report.Text())
		}
	}
}

func TestUnknownFieldsOutsideMainAndUserContentAreIgnored(t *testing.T) {
	dir, service := fixture(t)
	for _, name := range []string{"providers.toml", "state.toml", "elnis.toml", "tool_tags.toml", "plugins/hooks.toml"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "unknown_root = true\n"+string(data))
	}
	for _, name := range []string{"SOUL.md", "memories.toml", ".env.example", "plugins/.env", "skills/agent/user_added/SKILL.md"} {
		writeFile(t, filepath.Join(dir, name), "arbitrary user content\n")
	}
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
}

func TestChangedValuesAndArraysAreNotMissing(t *testing.T) {
	dir, service := fixture(t)
	main := filepath.Join(dir, "app.toml")
	editFile(t, main, "max_retries = 3", "max_retries = 0")
	editFile(t, main, "compact_enabled = true", "compact_enabled = false")
	editFile(t, main, `prefixes = ["/"]`, "prefixes = []")
	editFile(t, main, `log_level = "info"`, `log_level = ""`)
	writeFile(t, filepath.Join(dir, "plugins/hooks.toml"), "rules = []\n")
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	writeFile(t, filepath.Join(dir, "plugins/hooks.toml"), "[[rules]]\nname = 'a'\n[[rules]]\nname = 'a'\n")
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
}

func TestParseAndTypeErrorsDoNotBlockOtherFiles(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"syntax", "[providers\n", "TOML 格式错误（第"},
		{"duplicate key", "[providers.custom]\nbase_url = 'a'\nbase_url = 'b'\n", "重复定义"},
		{"duplicate table", "[providers.custom]\n[providers.custom]\n", "重复定义"},
		{"type", "[model_metadata]\ndefault_context_window = 'wrong'\n", "字段类型错误"},
		{"provider type", "[providers.custom]\nmodels = 123\n", "字段类型错误"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			path := filepath.Join(dir, "providers.toml")
			writeFile(t, path, tc.content)
			if err := os.Remove(filepath.Join(dir, "elnis.toml")); err != nil {
				t.Fatal(err)
			}
			report := check(t, service)
			if len(report.Files) != 2 || report.Files[0].Path != path || !strings.Contains(report.Files[0].Issues[0].Message, tc.want) {
				t.Fatal(report.Text())
			}
		})
	}
}

func TestInvalidMainDoesNotGuessDependentPaths(t *testing.T) {
	dir, service := fixture(t)
	writeFile(t, filepath.Join(dir, "app.toml"), "[invalid\n")
	if err := os.Remove(filepath.Join(dir, "providers.toml")); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(dir, "skills", "agent", "write_elbot_hook", "SKILL.md")
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	report := check(t, service)
	if len(report.Files) != 2 || report.Files[1].Path != skill || !strings.Contains(report.Text(), "未检查依赖") {
		t.Fatal(report.Text())
	}
	writeFile(t, filepath.Join(dir, "app.toml"), "config_files = 123\n")
	report = check(t, service)
	for _, file := range report.Files {
		if file.Path == filepath.Join(dir, "providers.toml") {
			t.Fatal("guessed dependent path after invalid config_files")
		}
	}
}

func TestMissingDirectoryIsNotCreated(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "custom.toml")
	service, err := New(path, testInspector())
	if err != nil {
		t.Fatal(err)
	}
	report := check(t, service)
	if len(report.Files) != 8 || report.Files[0].Path != path {
		t.Fatal(report.Text())
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created directory: %v", err)
	}
}

func TestSkillLineEndingsAndMalformedMetadata(t *testing.T) {
	dir, service := fixture(t)
	for _, asset := range config.DefaultAssets() {
		if asset.CompareContent {
			writeFile(t, filepath.Join(dir, asset.Path), strings.ReplaceAll(asset.Content, "\n", "\r\n"))
		}
	}
	if report := check(t, service); report.Text() != "Everything is OK" {
		t.Fatal(report.Text())
	}
	path := filepath.Join(dir, "skills", "agent", "agent_skill_creator", "ELBOT_SKILL.toml")
	writeFile(t, path, "[invalid\n")
	report := check(t, service)
	if len(report.Files) != 1 || len(report.Files[0].Issues) != 1 || report.Files[0].Issues[0].Level != config.LevelError || report.Files[0].Path != path {
		t.Fatal(report.Text())
	}
}

func TestReadFailureAndCancellation(t *testing.T) {
	dir, service := fixture(t)
	path := filepath.Join(dir, "providers.toml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if report := check(t, service); len(report.Files) != 1 || !strings.Contains(report.Text(), "无法读取文件") {
		t.Fatal(report.Text())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := New("", testInspector()); err == nil {
		t.Fatal("accepted empty main config path")
	}
}

func TestReportTextMatchesApprovedWording(t *testing.T) {
	report := Report{ConfigPath: "/srv/mybot/custom.toml", Files: []FileIssue{
		{Path: "/srv/mybot/custom.toml", Issues: []config.Issue{
			{Level: config.LevelError, Message: "TOML 格式错误。"},
			{Level: config.LevelHint, Message: "未完成相关配置检查。"},
		}},
		{Path: "/srv/mybot/skills/agent/agent_skill_creator/SKILL.md", Issues: []config.Issue{
			{Level: config.LevelHint, Message: "内容与当前内置版本不同，需要查看差异。"},
		}},
	}}
	want := `发现 1 项错误、2 项提示，涉及 2 个文件。请复制以下内容给 Elbot：

请参考以下资料，处理列出的 ElBot 配置问题：
配置说明：https://raw.githubusercontent.com/Elflare/elbot/main/docs/configuration.md
默认模板：https://raw.githubusercontent.com/Elflare/elbot/main/internal/config/assets.go

主配置：/srv/mybot/custom.toml

检查结果：
1. /srv/mybot/custom.toml
   - [错误] TOML 格式错误。
   - [提示] 未完成相关配置检查。

2. /srv/mybot/skills/agent/agent_skill_creator/SKILL.md
   - [提示] 内容与当前内置版本不同，需要查看差异。

修改前请备份；补齐必要配置及对应注释，保留已有配置值和注释；可选项先说明作用，由用户决定是否补充；格式错误、重复定义和未知字段先说明处理建议，Skill 差异只说明、不覆盖。`
	if got := report.Text(); got != want {
		t.Fatalf("unexpected text:\n%s", got)
	}
}

func TestContentComparisonIgnoresOnlyNewlines(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         func(string) string
		wantDifference bool
	}{
		{"CRLF", func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }, false},
		{"CR", func(s string) string { return strings.ReplaceAll(s, "\n", "\r") }, false},
		{"blank lines", func(s string) string { return strings.ReplaceAll(s, "\n", "\n\n\n") }, false},
		{"no newlines", func(s string) string { return strings.ReplaceAll(s, "\n", "") }, false},
		{"no final newline", func(s string) string { return strings.TrimRight(s, "\r\n") }, false},
		{"shifted newlines", func(s string) string { return strings.Join(strings.Split(strings.ReplaceAll(s, "\n", ""), ""), "\r\n") }, false},
		{"text", func(s string) string { return s + "changed" }, true},
		{"space", func(s string) string { return s + " " }, true},
		{"indent", func(s string) string { return "\t" + s }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, service := fixture(t)
			path := filepath.Join(dir, "skills", "agent", "agent_skill_creator", "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := tc.change(string(data))
			writeFile(t, path, changed)
			report := check(t, service)
			if tc.wantDifference {
				requireIssue(t, report, path, config.LevelHint, "内容与当前内置版本不同")
			} else if report.Text() != "Everything is OK" {
				t.Fatal(report.Text())
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != changed {
				t.Fatal("inspection modified file")
			}
		})
	}
}

func TestNewlineComparisonDoesNotHideInvalidTOML(t *testing.T) {
	dir, service := fixture(t)
	path := filepath.Join(dir, "skills", "agent", "agent_skill_creator", "ELBOT_SKILL.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Removing this CR would reproduce the bundled file exactly.
	writeFile(t, path, strings.Replace(string(data), "risk", "ri\rsk", 1))
	report := check(t, service)
	requireErrorCount(t, report, 1)
	requireIssue(t, report, path, config.LevelError, "TOML 格式错误")
}

func TestPartialMainParseDoesNotSupplyPaths(t *testing.T) {
	dir, service := fixture(t)
	main := filepath.Join(dir, "app.toml")
	writeFile(t, main, "[config_files]\nproviders='invented.toml'\n[invalid\n")
	report := check(t, service)
	requireErrorCount(t, report, 1)
	for _, file := range report.Files {
		if file.Path != main {
			t.Fatal(report.Text())
		}
	}
}

func testInspector() *config.Inspector {
	definitions := append(builtin.ConfigDefinitions(), rules.ConfigDefinition())
	return config.NewInspector(definitions...)
}
