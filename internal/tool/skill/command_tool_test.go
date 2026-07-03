package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestCommandArgumentsBuildsFlagArgs(t *testing.T) {
	manifest := AgentSkillManifest{Args: map[string]string{"input": "--input", "mode": "--mode"}, Parameters: map[string]any{"type": "object", "required": []any{"input"}, "properties": map[string]any{"input": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}}}
	args, err := commandArguments(manifest, json.RawMessage(`{"input":"hello","mode":"fast"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if got != "--input hello --mode fast" {
		t.Fatalf("args = %q", got)
	}
}

func TestCommandToolRunsCommandAndReturnsText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "echo.sh"), []byte("#!/bin/sh\necho input=$2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := CommandTool{Record: Record{Name: "echo_skill", Description: "echo", Kind: KindAgent, Root: root}, Manifest: AgentSkillManifest{Risk: tool.RiskLow, Command: []string{"sh", "echo.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "required": []any{"input"}, "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, err := cmd.Call(context.Background(), tool.CallRequest{Name: "echo_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Content) != "input=hello" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestCommandToolRunsCommandAndParsesJSONContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "json.sh"), []byte("#!/bin/sh\nprintf '{\"content\":\"ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := CommandTool{Record: Record{Name: "json_skill", Description: "json", Kind: KindAgent, Root: root}, Manifest: AgentSkillManifest{Risk: tool.RiskLow, Command: []string{"sh", "json.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, err := cmd.Call(context.Background(), tool.CallRequest{Name: "json_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q", result.Content)
	}
}
