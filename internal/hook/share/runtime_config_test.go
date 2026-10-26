package share

import (
	"strings"
	"testing"
)

func TestConfigValidateRequiresExplicitLifecycle(t *testing.T) {
	config := RuntimeConfig{
		Mode:                   RuntimeModePersistent,
		Command:                []string{"hook.exe"},
		Cwd:                    ".",
		StartupTimeoutSeconds:  5,
		ShutdownTimeoutSeconds: 5,
		EventTimeoutSeconds:    30,
		MaxWaitSeconds:         60,
		Restart: RuntimeRestartConfig{
			Strategy:            "on_failure",
			InitialDelaySeconds: 1,
			MaxDelaySeconds:     10,
		},
		ID:  "demo_hook",
		Dir: t.TempDir(),
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	config.Command = []string{""}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "runtime command is required") {
		t.Fatalf("Validate empty command: %v", err)
	}
	config.Command = []string{"hook.exe"}
	config.Restart.Strategy = ""
	if err := config.Validate(); err == nil {
		t.Fatal("Validate accepted missing restart strategy")
	}
}
