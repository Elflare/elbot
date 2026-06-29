package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestShellToolHasAgentTag(t *testing.T) {
	if got := strings.Join(NewShellTool().Info().Tags, ","); got != "agent" {
		t.Fatalf("shell tags = %q", got)
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

func TestShellToolWarnsForCat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "cat " + filepath.ToSlash(path)})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := tool.AppendWarnings(result.Content, result.Warnings)
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "read_file") {
		t.Fatalf("expected cat output and read_file warning, got:\n%s", text)
	}
}

func TestShellToolWarnsForCatElSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "reader", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#skill reader - Reader.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool(NewFileGuard(NewElSkillFileGuardRule(root)))
	args, _ := json.Marshal(map[string]any{"cmd": "cat " + filepath.ToSlash(path)})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := tool.AppendWarnings(result.Content, result.Warnings)
	if !strings.Contains(text, "read_file") || !strings.Contains(text, "read_el_skill") {
		t.Fatalf("expected read_file and read_el_skill warnings, got:\n%s", text)
	}
}

func TestShellToolRejectsSedEditElSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "writer", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "#skill writer - Writer.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool(NewFileGuard(NewElSkillFileGuardRule(root)))
	args, _ := json.Marshal(map[string]any{"cmd": "sed -i 's/Writer/Changed/' " + filepath.ToSlash(path)})
	_, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
		t.Fatalf("expected modify_el_skill error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestShellToolWarnsForCatResidentMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	if err := os.WriteFile(path, []byte("[[resident_memories]]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool(NewFileGuard(NewResidentMemoryFileGuardRule(path)))
	args, _ := json.Marshal(map[string]any{"cmd": "cat " + filepath.ToSlash(path)})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := tool.AppendWarnings(result.Content, result.Warnings)
	if !strings.Contains(text, "read_file") || !strings.Contains(text, "resident_memory_read") {
		t.Fatalf("expected read_file and resident_memory_read warnings, got:\n%s", text)
	}
}

func TestShellToolRejectsRedirectToResidentMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	original := "[[resident_memories]]\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool(NewFileGuard(NewResidentMemoryFileGuardRule(path)))
	args, _ := json.Marshal(map[string]any{"cmd": "echo changed > " + filepath.ToSlash(path)})
	_, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "resident_memory_normal") {
		t.Fatalf("expected resident memory error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestShellToolUsesSandboxDir(t *testing.T) {
	sandboxDir := filepath.Join(t.TempDir(), "sandbox", "cron")
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "pwd > cwd.txt"})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: sandboxDir, Background: true, BackgroundKind: tool.BackgroundKindCron})
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

func TestShellToolCancelReturnsQuickly(t *testing.T) {
	shell := NewShellTool()
	ctx, cancel := context.WithCancel(context.Background())
	args, _ := json.Marshal(map[string]any{"cmd": "sleep 5", "timeout_ms": 10000})
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := shell.Call(ctx, tool.CallRequest{Arguments: args})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("shell cancel took %s, want under 1s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("shell cancel did not return within 1s")
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

func TestResolveWindowsShellCachedAndValid(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	name1, args1 := resolveWindowsShell()
	name2, _ := resolveWindowsShell()
	if name1 != name2 {
		t.Fatalf("resolveWindowsShell not cached: %q vs %q", name1, name2)
	}
	if name1 == "" {
		t.Fatal("expected non-empty shell name")
	}
	if len(args1) == 0 {
		t.Fatal("expected non-empty shell args")
	}
	if name1 == "pwsh" || name1 == "powershell.exe" {
		if len(args1) < 2 || args1[0] != "-NoProfile" || args1[1] != "-Command" {
			t.Fatalf("unexpected powershell args: %v", args1)
		}
	}
	if name1 == "bash" {
		if len(args1) != 1 || args1[0] != "-lc" {
			t.Fatalf("unexpected bash args: %v", args1)
		}
	}
}
