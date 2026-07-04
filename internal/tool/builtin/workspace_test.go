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

func TestWorkspaceToolAcceptsHomeShortcut(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	args, _ := json.Marshal(map[string]any{"path": "~"})
	result, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != filepath.Clean(home) || !strings.Contains(result.Content, filepath.Clean(home)) {
		t.Fatalf("set content=%q stored=%q", result.Content, store.dir)
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

func TestWorkspaceToolLoadsAgentInstructionsOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("work rules"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Loaded workspace instructions from AGENTS.md:\nwork rules") {
		t.Fatalf("instructions not loaded: %q", result.Content)
	}
	if len(store.noticeDirs) != 1 || store.noticeDirs[0] != filepath.Clean(dir) {
		t.Fatalf("notice dirs = %#v", store.noticeDirs)
	}

	result, err = NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "work rules") {
		t.Fatalf("instructions repeated: %q", result.Content)
	}

	resumedStore := &testWorkspaceStore{noticeDirs: append([]string(nil), store.noticeDirs...)}
	resumedCtx := tool.WithWorkspaceStore(context.Background(), resumedStore)
	result, err = NewWorkspaceTool().Call(resumedCtx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "work rules") {
		t.Fatalf("instructions repeated after resume: %q", result.Content)
	}
}

func TestWorkspaceToolAgentInstructionNameRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("single"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.MD"), []byte("plural"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Loaded workspace instructions from AGENTS.MD:\nplural") || strings.Contains(result.Content, "single") {
		t.Fatalf("unexpected priority content: %q", result.Content)
	}
}

func TestWorkspaceToolIgnoresLowercaseAgentNameUntilValidFileExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agents.md"), []byte("lowercase"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "lowercase") || len(store.noticeDirs) != 0 {
		t.Fatalf("lowercase file should be ignored: content=%q notice=%#v", result.Content, store.noticeDirs)
	}

	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("valid"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err = NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Loaded workspace instructions from AGENT.md:\nvalid") {
		t.Fatalf("valid file not loaded: %q", result.Content)
	}
}

func TestWorkspaceToolSkipsLargeAgentInstructions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(strings.Repeat("x", maxWorkspaceAgentFileSize+1)), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{}
	ctx := tool.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, err := NewWorkspaceTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "larger than 64 KiB") || len(store.noticeDirs) != 0 {
		t.Fatalf("large file should warn without notice: content=%q notice=%#v", result.Content, store.noticeDirs)
	}
}
