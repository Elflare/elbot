package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestShellToolMissingCmdHintsExpectedArgument(t *testing.T) {
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"Command": "ls"})
	_, err := shell.AssessRisk(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), `use {"cmd":"..."}`) {
		t.Fatalf("AssessRisk error = %v", err)
	}
	_, err = shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), `use {"cmd":"..."}`) {
		t.Fatalf("Call error = %v", err)
	}
}

func TestShellToolRunsArbitraryCommand(t *testing.T) {
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "echo elbot-shell-test"})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !strings.Contains(result.Content, "elbot-shell-test") {
		t.Fatalf("unexpected shell result: %#v", result)
	}
}

func TestShellToolUsesSandboxDir(t *testing.T) {
	sandboxDir := filepath.Join(t.TempDir(), "sandbox", "cron")
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "pwd > cwd.txt"})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: sandboxDir, CronBackground: true})
	if _, err := shell.Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(sandboxDir, "cwd.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) == "" {
		t.Fatal("expected sandbox command to write cwd.txt")
	}
}

func TestShellToolRunsLS(t *testing.T) {

	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "ls", "timeout_ms": 5000})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected shell result")
	}
}
