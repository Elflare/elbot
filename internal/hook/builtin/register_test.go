package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/hook"
)

func TestRegisterAllKeepsRunningWhenRulesConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "bad"
on = "agent.out.prepared"
always = true
action = "append"
field = "message.text"
text = "!"
`
	if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rules config: %v", err)
	}
	manager := hook.NewManager()
	notices := []string{}
	configs, err := RegisterAll(manager, Options{
		ConfigDir: dir,
		Notify: func(ctx context.Context, text string) {
			notices = append(notices, text)
		},
	})
	if err == nil {
		t.Fatal("expected root Hook config error")
	}
	if len(configs) != 0 {
		t.Fatalf("runtime configs = %#v, want none", configs)
	}
	if len(notices) == 0 {
		t.Fatal("expected hook config notice")
	}
}
