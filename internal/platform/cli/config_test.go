package cli

import "testing"

func TestNewConfigFromPlatformConfig(t *testing.T) {
	raw := map[string]any{
		"default_client": "windows",
		"default_url":    "ws://192.168.1.10:32172/cli/v1/ws",
		"server": map[string]any{
			"enabled": true,
			"listen":  "0.0.0.0:32172",
			"tokens":  map[string]any{"windows": []any{"ELBOT_CLI_WINDOWS_TOKEN"}},
		},
		"clients": map[string]any{
			"windows": map[string]any{"token_env": []any{"ELBOT_CLI_WINDOWS_TOKEN"}},
		},
	}
	cfg, err := NewConfigFromPlatformConfig(raw)
	if err != nil {
		t.Fatalf("NewConfigFromPlatformConfig: %v", err)
	}
	if cfg.DefaultClient != "windows" {
		t.Fatalf("DefaultClient = %q", cfg.DefaultClient)
	}
	_, client, err := cfg.Client("")
	if err != nil {
		t.Fatalf("Client default: %v", err)
	}
	if client.ID != "windows" {
		t.Fatalf("client ID = %q", client.ID)
	}
	if client.URL != "ws://192.168.1.10:32172/cli/v1/ws" {
		t.Fatalf("client URL = %q", client.URL)
	}
}
