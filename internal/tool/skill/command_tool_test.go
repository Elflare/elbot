package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandArgumentsBuildsComplexFlagArgs(t *testing.T) {
	manifest := AgentSkillManifest{
		Args: map[string]string{"items": "--items", "options": "--options", "text": "--text", "number": "--number", "boolean": "--boolean"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"options": map[string]any{"type": "object"},
			},
		},
	}
	args, err := commandArguments(manifest, json.RawMessage(`{"options": {"width": 1024, "nested": {"enabled": true}}, "items": ["a", "b"], "text":"a \n b", "number":9007199254740993, "boolean":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--boolean", "true", "--items", `["a","b"]`, "--number", "9007199254740993", "--options", `{"width":1024,"nested":{"enabled":true}}`, "--text", "a \n b"}
	if len(args) != len(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
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
	cmd := commandTool{media: newTestMediaRuntime(t), record: record{Name: "echo_skill", Description: "echo", Kind: KindAgent, Root: root}, manifest: AgentSkillManifest{TimeoutSeconds: 30, Risk: toolshare.RiskLow, Command: []string{"sh", "echo.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "required": []any{"input"}, "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, _, err := callTool(context.Background(), cmd.Definition(), toolshare.CallRequest{Name: "echo_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Data.Content) != "input=hello" {
		t.Fatalf("content = %q", result.Data.Content)
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
	cmd := commandTool{media: newTestMediaRuntime(t), record: record{Name: "json_skill", Description: "json", Kind: KindAgent, Root: root}, manifest: AgentSkillManifest{TimeoutSeconds: 30, Risk: toolshare.RiskLow, Command: []string{"sh", "json.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, _, err := callTool(context.Background(), cmd.Definition(), toolshare.CallRequest{Name: "json_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if result.Data.Content != "ok" {
		t.Fatalf("content = %q", result.Data.Content)
	}
}
