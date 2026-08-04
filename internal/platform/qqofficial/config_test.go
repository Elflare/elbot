package qqofficial

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewFromPlatformConfigReadsClientSecretFromDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("QQ_SECRET=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("QQ_SECRET", "")

	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":           true,
		"app_id":            "app-id",
		"client_secret_env": "QQ_SECRET",
	}, nil, nil, nil, nil, dir, "", 0, 0)
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if got, want := adapter.cfg.ClientSecret, "from-dotenv"; got != want {
		t.Fatalf("client secret = %q, want %q", got, want)
	}
}

func TestNewFromPlatformConfigPrefersSystemEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("QQ_SECRET=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("QQ_SECRET", "from-environment")

	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":           true,
		"app_id":            "app-id",
		"client_secret_env": "QQ_SECRET",
	}, nil, nil, nil, nil, dir, "", 0, 0)
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if got, want := adapter.cfg.ClientSecret, "from-environment"; got != want {
		t.Fatalf("client secret = %q, want %q", got, want)
	}
}

func TestNewFromPlatformConfigPrefersExplicitClientSecret(t *testing.T) {
	t.Setenv("QQ_SECRET", "from-environment")

	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":           true,
		"app_id":            "app-id",
		"client_secret":     "explicit-secret",
		"client_secret_env": "QQ_SECRET",
	}, nil, nil, nil, nil, t.TempDir(), "", 0, 0)
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if got, want := adapter.cfg.ClientSecret, "explicit-secret"; got != want {
		t.Fatalf("client secret = %q, want %q", got, want)
	}
}
