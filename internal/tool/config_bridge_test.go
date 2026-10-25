package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool/builtin"
	toolshare "elbot/internal/tool/share"
)

func TestElwispCreatorGuideIncludesConfigSummary(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.toml")
	providersPath := filepath.Join(dir, "providers.toml")
	elnisPath := filepath.Join(dir, "elnis.toml")
	writeElwispCreatorTestFile(t, appPath, `
[config_files]
providers = "providers.toml"
elnis = "elnis.toml"

[mode_models.work]
provider = "test"
model = "work-model"

[mode_models.chat]
provider = "test"
model = "chat-model"
`)
	writeElwispCreatorTestFile(t, providersPath, `
[providers.test]
base_url = "https://example.invalid"
models = ["work-model", "chat-model"]
`)
	writeElwispCreatorTestFile(t, elnisPath, `
enabled = true
allowed_tools = ["web_extract", "web_search"]

[http]
addr = "127.0.0.1:45678"

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"]

`)

	result, _, err := builtin.NewElwispCreatorTool(configBridge{configPath: appPath}).Call(context.Background(), toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"endpoint: http://127.0.0.1:45678/elvena/v3/events",
		"token_env: [ELNIS_HOME_TOKEN]",
		"allowed_tools: [web_extract, web_search]",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("guide missing %q: %s", want, result.Content)
		}
	}
}

func TestElwispCreatorGuideWarnsWhenConfigMissing(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "missing.toml")
	cfg, err := (configBridge{configPath: appPath}).ElwispCreatorConfig()
	if !errors.Is(err, os.ErrNotExist) || cfg.AppConfigPath != appPath {
		t.Fatalf("config = %+v, err = %v", cfg, err)
	}

	result, _, err := builtin.NewElwispCreatorTool(configBridge{configPath: appPath}).Call(context.Background(), toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "读取 ElBot 配置失败") || !strings.Contains(result.Content, "修复配置") {
		t.Fatalf("guide missing config warning: %s", result.Content)
	}
}

func TestConfigBridgeReloadsOnlyConfiguredLocation(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.toml")
	writeElwispCreatorTestFile(t, appPath, `
[config_files]
providers = "providers.toml"
elnis = "elnis.toml"
[mode_models.work]
provider = "test"
model = "model"
[mode_models.chat]
provider = "test"
model = "model"
`)
	writeElwispCreatorTestFile(t, filepath.Join(dir, "providers.toml"), `
[providers.test]
base_url = "https://example.invalid"
models = ["model"]
`)
	elnisPath := filepath.Join(dir, "elnis.toml")
	writeElwispCreatorTestFile(t, elnisPath, "enabled = false\n[http]\naddr = '127.0.0.1:10001'\n")
	bridge := configBridge{configPath: appPath}
	first, err := bridge.ElwispCreatorConfig()
	if err != nil || first.Enabled || first.Addr != "127.0.0.1:10001" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	writeElwispCreatorTestFile(t, elnisPath, "enabled = true\n[http]\naddr = '127.0.0.1:10002'\n[tokens.home]\ntoken_env = ['SECRET_ENV']\n")
	t.Setenv("SECRET_ENV", "dummy-private-token")
	second, err := bridge.ElwispCreatorConfig()
	if err != nil || !second.Enabled || second.Addr != "127.0.0.1:10002" || strings.Join(second.TokenEnvNames, ",") != "SECRET_ENV" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	tool := builtin.NewElwispCreatorTool(bridge)
	result, _, err := tool.Call(t.Context(), toolshare.CallRequest{})
	if err != nil || strings.Contains(result.Content, "dummy-private-token") {
		t.Fatalf("guide = %+v, %v", result, err)
	}
}

func writeElwispCreatorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
