package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/workspace"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandSkillAsyncOption(t *testing.T) {
	const config = `command = ["example"]
parameters = '{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}'
positional = ["input"]
`
	for _, tc := range []struct {
		setting string
		mode    toolshare.AsyncMode
	}{
		{"", toolshare.AsyncDisabled},
		{"async = false", toolshare.AsyncDisabled},
		{"async = true", toolshare.SyncByDefault},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			manifest, err := ParseAgentSkillManifest([]byte(config + tc.setting))
			if err != nil {
				t.Fatal(err)
			}
			def := (commandTool{manifest: manifest}).Definition()
			param, ok := def.Schema().Parameters["properties"].(map[string]any)["async"]
			if def.Definition.Async != tc.mode || ok != (tc.mode == toolshare.SyncByDefault) {
				t.Fatalf("mode = %v, async property = %#v", def.Definition.Async, param)
			}
			if !ok {
				return
			}
			prop := param.(map[string]any)
			if prop["type"] != "boolean" || prop["default"] != false {
				t.Fatalf("async property = %#v", prop)
			}
			args, err := commandArguments(manifest, json.RawMessage(`{"async":true,"input":"hello"}`))
			if err != nil || len(args) != 1 || args[0] != "hello" {
				t.Fatalf("args = %v, err = %v", args, err)
			}
		})
	}
}

func TestCommandArgumentsBuildsComplexFlagArgs(t *testing.T) {
	manifest := AgentSkillManifest{
		Positional: []string{"text", "number"},
		Args:       map[string]string{"items": "--items", "options": "--options", "boolean": "--boolean"},
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
	want := []string{"a \n b", "9007199254740993", "--boolean", "true", "--items", `["a","b"]`, "--options", `{"width":1024,"nested":{"enabled":true}}`}
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
	cmd := commandTool{media: newTestMediaRuntime(t), record: record{Name: "echo_skill", Description: "echo", Root: root}, manifest: AgentSkillManifest{TimeoutSeconds: 30, Risk: toolshare.RiskLow, Command: []string{"sh", "echo.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "required": []any{"input"}, "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, _, err := callTool(context.Background(), cmd.Definition(), toolshare.CallRequest{Name: "echo_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Data.Content) != "input=hello" {
		t.Fatalf("content = %q", result.Data.Content)
	}
}

func TestCommandToolUsesWorkspaceForCommandAndMedia(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	skillRoot := t.TempDir()
	workspaceDir := t.TempDir()
	script := "#!/bin/sh\nprintf 'media-output' > generated.bin\nprintf '{\"segments\":[{\"type\":\"file\",\"path\":\"generated.bin\"}]}'\n"
	if err := os.WriteFile(filepath.Join(workspaceDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := AgentSkillManifest{
		UseWorkspace:   true,
		TimeoutSeconds: 30,
		Risk:           toolshare.RiskLow,
		Command:        []string{"sh", "run.sh"},
		Parameters:     map[string]any{"type": "object", "properties": map[string]any{}},
	}
	tool := commandTool{media: newTestMediaRuntime(t), record: record{Name: "workspace_skill", Root: skillRoot}, manifest: manifest}
	ctx := workspace.WithPathScope(context.Background(), workspace.PathScope{Dir: workspaceDir, Restricted: true})
	result, _, err := callTool(ctx, tool.Definition(), toolshare.CallRequest{Name: "workspace_skill", Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Status != operationresult.Success || len(result.Data.Segments) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Data.Segments[0].MediaID == "" {
		t.Fatalf("output media was not imported: %+v", result.Data.Segments[0])
	}
}

func TestCommandToolWorkspaceRequiresRestrictedDirectory(t *testing.T) {
	tool := commandTool{record: record{Name: "workspace_skill", Root: t.TempDir()}, manifest: AgentSkillManifest{UseWorkspace: true}}
	ctx := workspace.WithPathScope(context.Background(), workspace.PathScope{Restricted: true})
	prepared, err := tool.PrepareCall(ctx, toolshare.CallRequest{Name: "workspace_skill", Arguments: json.RawMessage(`{}`)})
	if err != nil || prepared.Status != operationresult.Unavailable {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
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
	cmd := commandTool{media: newTestMediaRuntime(t), record: record{Name: "json_skill", Description: "json", Root: root}, manifest: AgentSkillManifest{TimeoutSeconds: 30, Risk: toolshare.RiskLow, Command: []string{"sh", "json.sh"}, Args: map[string]string{"input": "--input"}, Parameters: map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}}}}
	result, _, err := callTool(context.Background(), cmd.Definition(), toolshare.CallRequest{Name: "json_skill", Arguments: json.RawMessage(`{"input":"hello"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if result.Data.Content != "ok" {
		t.Fatalf("content = %q", result.Data.Content)
	}
}
