package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestWorkspaceToolQuerySetAndReset(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	workspace := NewWorkspaceTool()

	result, err := workspace.Call(ctx, tool.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "current workspace:") {
		t.Fatalf("query content = %q", result.Content)
	}

	dir := t.TempDir()
	args, _ := json.Marshal(map[string]any{"path": dir})
	result, err = workspace.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != filepath.Clean(dir) || !strings.Contains(result.Content, "workspace set.") || !strings.Contains(result.Content, filepath.Clean(dir)) {
		t.Fatalf("set content=%q stored=%q", result.Content, store.dir)
	}

	args, _ = json.Marshal(map[string]any{"reset": true})
	result, err = workspace.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != "" || !strings.Contains(result.Content, "workspace reset.") || !strings.Contains(result.Content, "current workspace:") {
		t.Fatalf("reset content=%q stored=%q", result.Content, store.dir)
	}
}

func TestWorkspaceToolRejectsMissingDirectory(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "missing")})
	_, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected missing workspace error")
	}
}

func TestWorkspaceToolIsForegroundOnly(t *testing.T) {
	if !NewWorkspaceTool().Info().ForegroundOnly {
		t.Fatal("workspace tool must be foreground-only")
	}
}
