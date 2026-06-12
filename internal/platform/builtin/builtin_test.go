package builtin

import (
	"testing"

	"elbot/internal/config"
)

func TestNewRegistersEnabledQQOneBotOnly(t *testing.T) {
	cfg := &config.Config{
		Platform: config.PlatformConfig{
			"qqonebot": map[string]any{"enabled": false},
		},
	}
	bundle, err := New(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	if len(bundle.Runtimes) != 1 {
		t.Fatalf("disabled runtimes = %d, want cli only", len(bundle.Runtimes))
	}

	cfg.Platform["qqonebot"]["enabled"] = true
	bundle, err = New(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New enabled: %v", err)
	}
	if len(bundle.Runtimes) != 2 || bundle.Runtimes[1].Name() != "qqonebot" {
		t.Fatalf("enabled runtimes = %#v", bundle.Runtimes)
	}
}
