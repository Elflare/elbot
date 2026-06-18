package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestResolvePathUsesExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.toml")
	resolved, err := ResolvePath(path)
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if resolved != filepath.Clean(path) {
		t.Fatalf("resolved path = %q, want %q", resolved, filepath.Clean(path))
	}
}

func TestResolvePathUsesEnvConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env-app.toml")
	t.Setenv(EnvConfigFile, path)
	resolved, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if resolved != filepath.Clean(path) {
		t.Fatalf("resolved path = %q, want %q", resolved, filepath.Clean(path))
	}
}

func TestResolvePathGeneratesPlatformDefaultsWhenNoConfigExists(t *testing.T) {
	configHome := t.TempDir()
	setUserConfigDirEnv(t, configHome)
	t.Setenv(EnvConfigFile, "")
	t.Chdir(t.TempDir())

	resolved, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want, ok := platformDefaultConfigPath()
	if !ok {
		t.Fatal("platform default config path unavailable")
	}
	if resolved != filepath.Clean(want) {
		t.Fatalf("resolved path = %q, want %q", resolved, filepath.Clean(want))
	}
	for _, rel := range []string{"app.toml", "providers.toml", "state.toml", "SOUL.md", "elnis.toml", ".env.example"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(want), rel)); err != nil {
			t.Fatalf("expected generated file %s: %v", rel, err)
		}
	}
	for _, rel := range []string{filepath.Join("skills", "py"), filepath.Join("skills", "go"), "plugins", "long_memory"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(want), rel))
		if err != nil {
			t.Fatalf("expected generated dir %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", rel)
		}
	}
	cfg, err := Load(resolved)
	if err != nil {
		t.Fatalf("Load generated config: %v", err)
	}
	if cfg.ConfigPath != filepath.Clean(want) {
		t.Fatalf("generated ConfigPath = %q, want %q", cfg.ConfigPath, filepath.Clean(want))
	}
	if cfg.Elnis.Enabled {
		t.Fatal("generated Elnis config should be disabled")
	}
}

func TestEnsurePlatformDefaultsDoesNotOverwriteExistingFiles(t *testing.T) {
	configHome := t.TempDir()
	setUserConfigDirEnv(t, configHome)
	configPath, ok := platformDefaultConfigPath()
	if !ok {
		t.Fatal("platform default config path unavailable")
	}
	custom := "# custom app\n"
	writeFile(t, configPath, custom)

	generated, err := EnsurePlatformDefaults()
	if err != nil {
		t.Fatalf("EnsurePlatformDefaults: %v", err)
	}
	if generated != configPath {
		t.Fatalf("generated path = %q, want %q", generated, configPath)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("existing app.toml was overwritten: %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(configPath), "elnis.toml")); err != nil {
		t.Fatalf("expected missing assets to be created: %v", err)
	}
}

func TestLoadSplitConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	appPath := filepath.Join(configDir, "app.toml")
	providersPath := filepath.Join(configDir, "providers.toml")
	statePath := filepath.Join(configDir, "state.toml")
	toolTagsPath := filepath.Join(configDir, "tool_tags.toml")
	writeFile(t, appPath, `
[config_files]
providers = "providers.toml"
state = "state.toml"
tool_tags = "tool_tags.toml"

[soul]
path = "SOUL.md"

[storage]
sessions_sqlite_path = "../data/elbot_sessions.db"
chat_history_sqlite_path = "../data/elbot_chat_history.db"

[runtime]
log_level = "debug"
log_retention_days = 14

[context]
compact_enabled = true
compact_trigger_ratio = 0.75

[view]
session_list_page_size = 7

[commands]
prefixes = ["/", "-"]

[tools]
max_rounds_per_turn = 3

[maintenance.log_cleanup]
enabled = true
schedule = "0 4 * * *"

[maintenance.artifact_cleanup]
enabled = true
schedule = "0 5 * * *"

[sandbox]
root = "../data/sandbox"

[artifact]
retention_days = 9
max_direct_base64_bytes = 123456
backend = "base64"
s3_endpoint = "https://r2.example"
s3_region = "auto"
s3_bucket = "elbot-artifacts"
s3_access_key_env = "ELBOT_TEST_S3_ACCESS"
s3_secret_key_env = "ELBOT_TEST_S3_SECRET"
s3_public_base_url = "https://files.example"

[platform.qqonebot]
enabled = true
ws_url = "ws://example"
trigger_keywords = ["芙莉丝"]

[session]
non_superadmin_idle_ttl_minutes = 0

[session.naming]
trigger_step = 3

[session.cleanup]
enabled = true
retention_days = 14
`)
	writeFile(t, providersPath, `
[global_default]
stream = true
temperature = 0.7
max_tokens = 1024

[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key = "${DUMMY_API_KEY}"
models = ["deepseek-v4-flash"]
extra_payload = { provider_field = "provider" }

[providers.deepseek.model_configs."deepseek-v4-flash"]
extra_payload = { thinking = { type = "disabled" }, provider_field = "model" }

[model_metadata]
default_context_window = 12345

[model_metadata.context_windows]
deepseek-v4-flash = 64000
`)
	writeFile(t, statePath, `
[session]
default_mode = "chat"

[mode_models.work]
provider = "deepseek"
model = "deepseek-state"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat-state"

[naming_model]
provider = "deepseek"
model = "deepseek-title"

[compact_model]
provider = "deepseek"
model = "deepseek-compact"
`)
	writeFile(t, toolTagsPath, `
[tags.web]
tools = ["web_search", "web_extract"]
prompt = "Use web tools."

[tags.agent]
tools = ["read_file", "shell"]
prompt = "Use agent tools."
`)

	cfg, err := Load(appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ConfigPath != filepath.Clean(appPath) {
		t.Fatalf("ConfigPath = %q, want %q", cfg.ConfigPath, filepath.Clean(appPath))
	}
	if cfg.ProvidersConfigPath != filepath.Clean(providersPath) {
		t.Fatalf("ProvidersConfigPath = %q, want %q", cfg.ProvidersConfigPath, filepath.Clean(providersPath))
	}
	if cfg.StateConfigPath != filepath.Clean(statePath) {
		t.Fatalf("StateConfigPath = %q, want %q", cfg.StateConfigPath, filepath.Clean(statePath))
	}
	if cfg.ToolTagsConfigPath != filepath.Clean(toolTagsPath) {
		t.Fatalf("ToolTagsConfigPath = %q, want %q", cfg.ToolTagsConfigPath, filepath.Clean(toolTagsPath))
	}
	wantToolTags := ToolTagsConfig{Tags: map[string]ToolTagConfig{
		"web":   {Tools: []string{"web_search", "web_extract"}, Prompt: "Use web tools."},
		"agent": {Tools: []string{"read_file", "shell"}, Prompt: "Use agent tools."},
	}}
	if !reflect.DeepEqual(cfg.ToolTags, wantToolTags) {
		t.Fatalf("ToolTags = %#v, want %#v", cfg.ToolTags, wantToolTags)
	}
	wantDB := filepath.Clean(filepath.Join(configDir, "../data/elbot_sessions.db"))
	if cfg.Storage.SessionsSQLitePath != wantDB {
		t.Fatalf("SessionsSQLitePath = %q, want %q", cfg.Storage.SessionsSQLitePath, wantDB)
	}
	wantChatHistoryDB := filepath.Clean(filepath.Join(configDir, "../data/elbot_chat_history.db"))
	if cfg.Storage.ChatHistorySQLitePath != wantChatHistoryDB {
		t.Fatalf("ChatHistorySQLitePath = %q, want %q", cfg.Storage.ChatHistorySQLitePath, wantChatHistoryDB)
	}
	if cfg.Runtime.LogLevel != "debug" || cfg.Runtime.LogRetentionDays != 14 {
		t.Fatalf("runtime = %#v", cfg.Runtime)
	}
	if !cfg.Context.CompactEnabled {
		t.Fatal("CompactEnabled = false")
	}
	if cfg.Context.CompactTriggerRatio != 0.75 {
		t.Fatalf("CompactTriggerRatio = %v", cfg.Context.CompactTriggerRatio)
	}
	if cfg.ModelMetadata.DefaultContextWindow != 12345 {
		t.Fatalf("DefaultContextWindow = %d", cfg.ModelMetadata.DefaultContextWindow)
	}
	if !reflect.DeepEqual(cfg.Commands.Prefixes, []string{"/", "-"}) {
		t.Fatalf("Command prefixes = %#v", cfg.Commands.Prefixes)
	}
	if cfg.View.SessionListPageSize != 7 {
		t.Fatalf("session list page size = %d", cfg.View.SessionListPageSize)
	}
	if cfg.Tools.MaxRoundsPerTurn != 3 {
		t.Fatalf("max tool rounds per turn = %d", cfg.Tools.MaxRoundsPerTurn)
	}
	wantCleanup := SessionCleanupConfig{Enabled: true, RetentionDays: 14}
	if !reflect.DeepEqual(cfg.Session.Cleanup, wantCleanup) {
		t.Fatalf("cleanup = %#v, want %#v", cfg.Session.Cleanup, wantCleanup)
	}
	wantMaintenance := MaintenanceConfig{
		LogCleanup:         CronTaskConfig{Enabled: true, Schedule: "0 4 * * *"},
		ArtifactCleanup:    CronTaskConfig{Enabled: true, Schedule: "0 5 * * *"},
		ChatHistoryCleanup: ChatHistoryCleanupConfig{Schedule: "0 35 4 * * *", RetentionDays: 180},
	}
	if !reflect.DeepEqual(cfg.Maintenance, wantMaintenance) {
		t.Fatalf("maintenance = %#v, want %#v", cfg.Maintenance, wantMaintenance)
	}
	if cfg.Sandbox.Root != filepath.Clean(filepath.Join(configDir, "../data/sandbox")) {
		t.Fatalf("sandbox root = %q", cfg.Sandbox.Root)
	}
	wantArtifact := ArtifactConfig{RetentionDays: 9, MaxDirectBase64Bytes: 123456, Backend: "base64", S3Endpoint: "https://r2.example", S3Region: "auto", S3Bucket: "elbot-artifacts", S3AccessKeyEnv: "ELBOT_TEST_S3_ACCESS", S3SecretKeyEnv: "ELBOT_TEST_S3_SECRET", S3PublicBaseURL: "https://files.example"}
	if !reflect.DeepEqual(cfg.Artifact, wantArtifact) {
		t.Fatalf("artifact = %#v, want %#v", cfg.Artifact, wantArtifact)
	}
	wantNaming := SessionNamingConfig{TriggerStep: 3}
	if !reflect.DeepEqual(cfg.Session.Naming, wantNaming) {
		t.Fatalf("naming = %#v, want %#v", cfg.Session.Naming, wantNaming)
	}
	qqConfig := cfg.Platform["qqonebot"]
	if qqConfig["enabled"] != true || qqConfig["ws_url"] != "ws://example" {
		t.Fatalf("qq onebot platform config = %#v", qqConfig)
	}
	keywords, ok := qqConfig["trigger_keywords"].([]any)
	if !ok || len(keywords) != 1 || keywords[0] != "芙莉丝" {
		t.Fatalf("trigger keywords = %#v", qqConfig["trigger_keywords"])
	}
	if _, ok := cfg.Platform["qq_onebot"]; ok {
		t.Fatal("config should not keep legacy qq_onebot platform name")
	}
	if cfg.Session.NonSuperadminIdleTTLMinutes != 0 {
		t.Fatalf("non-superadmin idle ttl = %d, want disabled", cfg.Session.NonSuperadminIdleTTLMinutes)
	}
	if cfg.NamingModel.Provider != "deepseek" || cfg.NamingModel.Model != "deepseek-title" {
		t.Fatalf("naming model = %q/%q", cfg.NamingModel.Provider, cfg.NamingModel.Model)
	}
	if cfg.CompactModel.Provider != "deepseek" || cfg.CompactModel.Model != "deepseek-compact" {
		t.Fatalf("compact model = %q/%q", cfg.CompactModel.Provider, cfg.CompactModel.Model)
	}
	if cfg.Session.DefaultMode != "chat" {
		t.Fatalf("default mode = %q", cfg.Session.DefaultMode)
	}
	if cfg.ModeModels["work"].Provider != "deepseek" || cfg.ModeModels["work"].Model != "deepseek-state" {
		t.Fatalf("work model = %q/%q", cfg.ModeModels["work"].Provider, cfg.ModeModels["work"].Model)
	}
	if cfg.ModeModels["chat"].Provider != "deepseek" || cfg.ModeModels["chat"].Model != "deepseek-chat-state" {
		t.Fatalf("chat model = %q/%q", cfg.ModeModels["chat"].Provider, cfg.ModeModels["chat"].Model)
	}
	if cfg.Soul.Path != filepath.Clean(filepath.Join(configDir, "SOUL.md")) {
		t.Fatalf("Soul.Path = %q", cfg.Soul.Path)
	}
	provider := cfg.Providers["deepseek"]
	if provider.APIKey != "${DUMMY_API_KEY}" {
		t.Fatalf("provider api key = %q", provider.APIKey)
	}
	if provider.ExtraPayload["provider_field"] != "provider" {
		t.Fatalf("provider extra payload = %#v", provider.ExtraPayload)
	}
	modelCfg := provider.ModelConfigs["deepseek-v4-flash"]
	thinking, ok := modelCfg.ExtraPayload["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" || modelCfg.ExtraPayload["provider_field"] != "model" {
		t.Fatalf("model config extra payload = %#v", modelCfg.ExtraPayload)
	}
	if cfg.ModelMetadata.ContextWindows["deepseek-v4-flash"] != 64000 {
		t.Fatalf("context window = %d", cfg.ModelMetadata.ContextWindows["deepseek-v4-flash"])
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	appPath := filepath.Join(configDir, "app.toml")
	providersPath := filepath.Join(configDir, "providers.toml")
	writeFile(t, appPath, ``)
	writeFile(t, providersPath, ``)
	writeFile(t, filepath.Join(configDir, "state.toml"), `
[mode_models.work]
provider = "deepseek"
model = "deepseek-v4-flash"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"
`)

	cfg, err := Load(appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ProvidersConfigPath != filepath.Clean(providersPath) {
		t.Fatalf("ProvidersConfigPath = %q", cfg.ProvidersConfigPath)
	}
	if cfg.StateConfigPath != filepath.Clean(filepath.Join(configDir, "state.toml")) {
		t.Fatalf("StateConfigPath = %q", cfg.StateConfigPath)
	}
	if cfg.Runtime.LogLevel != "info" || cfg.Runtime.LogRetentionDays != 30 {
		t.Fatalf("runtime defaults = %#v", cfg.Runtime)
	}
	if cfg.Context.CompactTriggerRatio != 0.8 {
		t.Fatalf("CompactTriggerRatio = %v", cfg.Context.CompactTriggerRatio)
	}
	if cfg.ModelMetadata.DefaultContextWindow != 8192 {
		t.Fatalf("DefaultContextWindow = %d", cfg.ModelMetadata.DefaultContextWindow)
	}
	if !reflect.DeepEqual(cfg.Commands.Prefixes, []string{"/"}) {
		t.Fatalf("Command prefixes = %#v", cfg.Commands.Prefixes)
	}
	if cfg.Tools.MaxRoundsPerTurn != 2 {
		t.Fatalf("default max tool rounds per turn = %d", cfg.Tools.MaxRoundsPerTurn)
	}
	if cfg.View.SessionListPageSize != 10 {
		t.Fatalf("default session list page size = %d", cfg.View.SessionListPageSize)
	}
	wantCleanup := SessionCleanupConfig{RetentionDays: 30}
	if !reflect.DeepEqual(cfg.Session.Cleanup, wantCleanup) {
		t.Fatalf("cleanup = %#v, want %#v", cfg.Session.Cleanup, wantCleanup)
	}
	if cfg.Maintenance.LogCleanup.Schedule != "0 3 * * *" || cfg.Maintenance.ArtifactCleanup.Schedule != "0 4 * * *" {
		t.Fatalf("maintenance defaults = %#v", cfg.Maintenance)
	}
	if cfg.Sandbox.Root != filepath.Clean(filepath.Join(platformDefaultDataDir(), "sandbox")) {
		t.Fatalf("sandbox root default = %q", cfg.Sandbox.Root)
	}
	wantArtifact := ArtifactConfig{RetentionDays: 7, MaxDirectBase64Bytes: 8 * 1024 * 1024, Backend: "base64", S3Region: "auto"}
	if !reflect.DeepEqual(cfg.Artifact, wantArtifact) {
		t.Fatalf("artifact defaults = %#v, want %#v", cfg.Artifact, wantArtifact)
	}
	if cfg.Session.Naming.TriggerStep != 1 {
		t.Fatalf("naming trigger step = %d", cfg.Session.Naming.TriggerStep)
	}
	if cfg.Session.DefaultMode != "work" {
		t.Fatalf("default mode = %q", cfg.Session.DefaultMode)
	}
	if cfg.Session.NonSuperadminIdleTTLMinutes != 10 {
		t.Fatalf("default non-superadmin idle ttl = %d", cfg.Session.NonSuperadminIdleTTLMinutes)
	}
	wantDB := filepath.Clean(filepath.Join(platformDefaultDataDir(), "elbot_sessions.db"))
	if cfg.Storage.SessionsSQLitePath != wantDB {
		t.Fatalf("SessionsSQLitePath = %q, want %q", cfg.Storage.SessionsSQLitePath, wantDB)
	}
	wantChatHistoryDB := filepath.Clean(filepath.Join(platformDefaultDataDir(), "elbot_chat_history.db"))
	if cfg.Storage.ChatHistorySQLitePath != wantChatHistoryDB {
		t.Fatalf("ChatHistorySQLitePath = %q, want %q", cfg.Storage.ChatHistorySQLitePath, wantChatHistoryDB)
	}
	if cfg.Soul.Path != filepath.Clean(filepath.Join(configDir, "SOUL.md")) {
		t.Fatalf("Soul.Path = %q", cfg.Soul.Path)
	}
	if cfg.Providers == nil {
		t.Fatal("Providers map is nil")
	}
	if cfg.ModelMetadata.ContextWindows == nil {
		t.Fatal("ContextWindows map is nil")
	}
}

func TestSaveState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "state.toml")
	state := StateConfig{
		Session: StateSessionConfig{DefaultMode: "chat"},
		ModeModels: map[string]ModelSelection{
			"work": {Provider: "zhipu", Model: "glm-4-flash"},
			"chat": {Provider: "zhipu", Model: "glm-4-air"},
		},
		NamingModel:  ModelSelection{Provider: "deepseek", Model: "deepseek-title"},
		CompactModel: ModelSelection{Provider: "zhipu", Model: "glm-compact"},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded := &StateConfig{}
	if err := loadTOML(path, loaded); err != nil {
		t.Fatalf("load saved state: %v", err)
	}
	if !reflect.DeepEqual(*loaded, state) {
		t.Fatalf("state = %#v, want %#v", *loaded, state)
	}
}

func TestLoadMissingAppConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	t.Fatalf("expected os.ErrNotExist, got %v", err)
}

func TestLoadMissingProvidersConfig(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.toml")
	writeFile(t, appPath, `
[config_files]
providers = "missing.toml"
`)

	_, err := Load(appPath)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	t.Fatalf("expected os.ErrNotExist, got %v", err)
}

func TestLoadProviderAPIKeyEnv(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	appPath := filepath.Join(configDir, "app.toml")
	writeFile(t, appPath, ``)
	writeFile(t, filepath.Join(configDir, "providers.toml"), `
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "ELBOT_TEST_DEEPSEEK_API_KEY"
`)
	writeFile(t, filepath.Join(configDir, "state.toml"), `
[mode_models.work]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"
`)
	writeFile(t, filepath.Join(configDir, ".env"), `ELBOT_TEST_DEEPSEEK_API_KEY=from-dotenv
`)

	cfg, err := Load(appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "from-dotenv" {
		t.Fatalf("api key = %q", cfg.Providers["deepseek"].APIKey)
	}
}

func TestLoadProviderAPIKeyEnvMissingDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	appPath := filepath.Join(configDir, "app.toml")
	writeFile(t, appPath, ``)
	writeFile(t, filepath.Join(configDir, "providers.toml"), `
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "ELBOT_TEST_MISSING_API_KEY"
`)
	writeFile(t, filepath.Join(configDir, "state.toml"), `
[mode_models.work]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"
`)

	cfg, err := Load(appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "" {
		t.Fatalf("api key = %q", cfg.Providers["deepseek"].APIKey)
	}
}

func TestLoadProviderAPIKeyEnvPrefersOS(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	appPath := filepath.Join(configDir, "app.toml")
	writeFile(t, appPath, ``)
	writeFile(t, filepath.Join(configDir, "providers.toml"), `
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "ELBOT_TEST_DEEPSEEK_API_KEY"
`)
	writeFile(t, filepath.Join(configDir, "state.toml"), `
[mode_models.work]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"
`)
	writeFile(t, filepath.Join(configDir, ".env"), `ELBOT_TEST_DEEPSEEK_API_KEY=from-dotenv
`)
	t.Setenv("ELBOT_TEST_DEEPSEEK_API_KEY", "from-os")

	cfg, err := Load(appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "from-os" {
		t.Fatalf("api key = %q", cfg.Providers["deepseek"].APIKey)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setUserConfigDirEnv(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
		t.Setenv("APPDATA", dir)
		return
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}
