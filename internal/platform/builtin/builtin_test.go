package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/config"
)

func TestNewRegistersEnabledQQOneBotOnly(t *testing.T) {
	cfg := &config.Config{
		Platform: config.PlatformConfig{
			"qqonebot": map[string]any{"enabled": false},
		},
	}
	bundle, err := New(Options{Mode: ModeFull}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	if len(bundle.Runtimes) != 1 {
		t.Fatalf("disabled runtimes = %d, want cli only", len(bundle.Runtimes))
	}

	cfg.Platform["qqonebot"]["enabled"] = true
	bundle, err = New(Options{Mode: ModeFull}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New enabled: %v", err)
	}
	if len(bundle.Runtimes) != 2 || bundle.Runtimes[1].Name() != "qqonebot" {
		t.Fatalf("enabled runtimes = %#v", bundle.Runtimes)
	}
}

func TestNewQQOfficialReadsClientSecretFromConfigDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("QQ_SECRET=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("QQ_SECRET", "")
	cfg := &config.Config{
		ConfigPath: filepath.Join(dir, "app.toml"),
		Platform: config.PlatformConfig{
			"qqofficial": map[string]any{
				"enabled":           true,
				"app_id":            "app-id",
				"client_secret_env": "QQ_SECRET",
			},
		},
	}

	bundle, err := New(Options{Mode: ModeFull}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(bundle.Runtimes) != 2 || bundle.Runtimes[1].Name() != "qqofficial" {
		t.Fatalf("runtimes = %#v, want cli and qqofficial", bundle.Runtimes)
	}
}

func TestNewQQOneBotReadsAccessTokenFromConfigDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("QQONEBOT_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("QQONEBOT_TOKEN", "")
	cfg := &config.Config{
		ConfigPath: filepath.Join(dir, "app.toml"),
		Platform: config.PlatformConfig{
			"qqonebot": map[string]any{
				"enabled":          true,
				"access_token_env": "QQONEBOT_TOKEN",
			},
		},
	}

	bundle, err := New(Options{Mode: ModeFull}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(bundle.Runtimes) != 2 || bundle.Runtimes[1].Name() != "qqonebot" {
		t.Fatalf("runtimes = %#v, want cli and qqonebot", bundle.Runtimes)
	}
}

func TestNewModes(t *testing.T) {
	cfg := &config.Config{
		Platform: config.PlatformConfig{
			"qqonebot": map[string]any{"enabled": true},
		},
	}

	cliOnly, err := New(Options{Mode: ModeCLIOnly}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New cli-only: %v", err)
	}
	if cliOnly.Primary.Name() != "cli" || len(cliOnly.Runtimes) != 1 || cliOnly.Runtimes[0].Name() != "cli" {
		t.Fatalf("cli-only bundle = %#v", cliOnly)
	}

	service, err := New(Options{Mode: ModeService}, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if service.Primary.Name() != "service" {
		t.Fatalf("service primary = %q, want service", service.Primary.Name())
	}
	if len(service.Runtimes) != 1 || service.Runtimes[0].Name() != "qqonebot" {
		t.Fatalf("service runtimes = %#v", service.Runtimes)
	}
}
