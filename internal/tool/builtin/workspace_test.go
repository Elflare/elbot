package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/processenv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/workspace"
)

func TestWorkspaceToolQuerySetAndReset(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	workspace := NewWorkspaceTool()

	result, _, err := callTool(ctx, workspace, toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "current workspace:") {
		t.Fatalf("query content = %q", result.Content)
	}

	dir := t.TempDir()
	args, _ := json.Marshal(map[string]any{"path": dir})
	result, _, err = callTool(ctx, workspace, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != filepath.Clean(dir) || !strings.Contains(result.Content, "workspace set.") || !strings.Contains(result.Content, filepath.Clean(dir)) {
		t.Fatalf("set content=%q stored=%q", result.Content, store.dir)
	}

	args, _ = json.Marshal(map[string]any{"reset": true})
	result, _, err = callTool(ctx, workspace, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != "" || !strings.Contains(result.Content, "workspace reset.") || !strings.Contains(result.Content, "current workspace:") {
		t.Fatalf("reset content=%q stored=%q", result.Content, store.dir)
	}
}

func TestWorkspaceToolAcceptsHomeShortcut(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, path := range []string{"~", "$HOME"} {
		args, _ := json.Marshal(map[string]any{"path": path})
		result, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
		if err != nil {
			t.Fatalf("path %q: %v", path, err)
		}
		if store.dir != filepath.Clean(home) || !strings.Contains(result.Content, filepath.Clean(home)) {
			t.Fatalf("path %q: set content=%q stored=%q", path, result.Content, store.dir)
		}
	}
}

func TestWorkspaceToolAcceptsHomeSubpath(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	subdir := filepath.Join(home, "workspace")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"$HOME/workspace", `$HOME\workspace`} {
		args, _ := json.Marshal(map[string]any{"path": path})
		_, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
		if err != nil {
			t.Fatalf("path %q: %v", path, err)
		}
		if store.dir != filepath.Clean(subdir) {
			t.Fatalf("path %q: stored=%q want=%q", path, store.dir, subdir)
		}
	}
}

func TestWorkspaceToolDoesNotExpandHomePrefixWithoutBoundary(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	args, _ := json.Marshal(map[string]any{"path": "$HOMEfoo"})
	_, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected literal $HOMEfoo path to be rejected")
	}
}

func TestWorkspaceToolRejectsMissingDirectory(t *testing.T) {
	store := &testWorkspaceStore{}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "missing")})
	_, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
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
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, logContent, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Loaded workspace instructions from AGENTS.md:\nwork rules") {
		t.Fatalf("instructions not loaded: %q", result.Content)
	}
	if strings.Contains(logContent, "work rules") || strings.Contains(logContent, "AGENTS.md") || !strings.Contains(logContent, "workspace set.") || len([]rune(logContent)) > 1024 {
		t.Fatalf("workspace log included instructions: %q", logContent)
	}
	if len(store.noticeDirs) != 1 || store.noticeDirs[0] != filepath.Clean(dir) {
		t.Fatalf("notice dirs = %#v", store.noticeDirs)
	}

	result, _, err = callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "work rules") {
		t.Fatalf("instructions repeated: %q", result.Content)
	}

	resumedStore := &testWorkspaceStore{noticeDirs: append([]string(nil), store.noticeDirs...)}
	resumedCtx := workspace.WithWorkspaceStore(context.Background(), resumedStore)
	result, _, err = callTool(resumedCtx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "work rules") {
		t.Fatalf("instructions repeated after resume: %q", result.Content)
	}
}

func TestWorkspaceToolDiscoveryLoadsAgentInstructionsOnceAcrossEntryPoints(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("discovery rules"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{dir: dir}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	workspace := NewWorkspaceTool()

	content, override, err := workspace.DiscoveryContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if override || !strings.Contains(content, "Loaded workspace instructions from AGENTS.md:\ndiscovery rules") {
		t.Fatalf("unexpected discovery content: override=%t content=%q", override, content)
	}
	if len(store.noticeDirs) != 1 || store.noticeDirs[0] != filepath.Clean(dir) {
		t.Fatalf("notice dirs = %#v", store.noticeDirs)
	}

	content, _, err = workspace.DiscoveryContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Fatalf("instructions repeated during discovery: %q", content)
	}

	args, _ := json.Marshal(map[string]any{"path": dir})
	result, _, err := callTool(ctx, workspace, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "discovery rules") {
		t.Fatalf("instructions repeated while setting workspace: %q", result.Content)
	}
}

func TestDiscoverWorkspaceLoadsCurrentAgentInstructions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("current rules"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &testWorkspaceStore{dir: dir}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	ctx = security.WithPermission(ctx, security.NewPermission("low", "critical", map[string][]string{"cli": {"admin"}}))
	ctx = contextinfo.WithActor(ctx, contextinfo.Actor{ID: "cli:admin", Platform: "cli", PlatformUserID: "admin", Role: contextinfo.RoleSuperadmin})
	registry := toolregistry.NewRegistry(security.NewPermission("critical", "critical", nil))
	if err := registry.Register(NewWorkspaceTool()); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "workspace"})
	result, _, err := callTool(ctx, discovery.NewDiscoverTool(discovery.NewResolver(registry)), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "已发现：workspace") || !strings.Contains(result.Content, "Loaded workspace instructions from AGENT.md:\ncurrent rules") {
		t.Fatalf("unexpected discovery result: %q", result.Content)
	}

	result, _, err = callTool(ctx, discovery.NewDiscoverTool(discovery.NewResolver(registry)), toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "current rules") {
		t.Fatalf("list discovery should not load instructions: %q", result.Content)
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
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
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
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "lowercase") || len(store.noticeDirs) != 0 {
		t.Fatalf("lowercase file should be ignored: content=%q notice=%#v", result.Content, store.noticeDirs)
	}

	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("valid"), 0644); err != nil {
		t.Fatal(err)
	}
	result, _, err = callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
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
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"path": dir})

	result, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "larger than 64 KiB") || len(store.noticeDirs) != 0 {
		t.Fatalf("large file should warn without notice: content=%q notice=%#v", result.Content, store.noticeDirs)
	}
}

func TestWorkspaceToolResetLoadsAgentInstructionsOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("default rules"), 0644); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	}()

	store := &testWorkspaceStore{dir: filepath.Join(t.TempDir(), "other")}
	ctx := workspace.WithWorkspaceStore(context.Background(), store)
	args, _ := json.Marshal(map[string]any{"reset": true})

	result, _, err := callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if store.dir != "" || !strings.Contains(result.Content, "Loaded workspace instructions from AGENT.md:\ndefault rules") {
		t.Fatalf("reset should clear workspace and load instructions: content=%q stored=%q", result.Content, store.dir)
	}
	if len(store.noticeDirs) != 1 || store.noticeDirs[0] != filepath.Clean(dir) {
		t.Fatalf("notice dirs = %#v", store.noticeDirs)
	}

	result, _, err = callTool(ctx, NewWorkspaceTool(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "default rules") {
		t.Fatalf("reset instructions repeated: %q", result.Content)
	}
}

func TestDiscoverToolsResetsMissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("fallback rules"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace", "shell", "read_file"} {
		t.Run(name, func(t *testing.T) {
			store := &testWorkspaceStore{dir: filepath.Join(t.TempDir(), "missing")}
			ctx := workspace.WithWorkspaceStore(context.Background(), store)
			ctx = contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
			registry := toolregistry.NewRegistry(security.NewPermission("critical", "critical", nil))
			for _, item := range []toolshare.Tool{NewWorkspaceTool(), NewShellTool(processenv.Environment{}, nil), NewReadFileTool(nil), NewRollbackFileTool(nil)} {
				if err := registry.Register(item); err != nil {
					t.Fatal(err)
				}
			}
			args, _ := json.Marshal(map[string]string{"name": name})
			result, _, err := callTool(ctx, discovery.NewDiscoverTool(discovery.NewResolver(registry)), toolshare.CallRequest{Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if store.dir != "" || !strings.Contains(result.Content, "fallback rules") {
				t.Fatalf("workspace=%q result=%q", store.dir, result.Content)
			}
			var discovered toolshare.DiscoveryResult
			if err := json.Unmarshal(result.Data, &discovered); err != nil {
				t.Fatal(err)
			}
			if len(discovered.Errors) != 0 {
				t.Fatalf("discovery errors: %+v", discovered.Errors)
			}
			found := false
			for _, item := range discovered.Tools {
				if item.Info.Name == name && item.Schema != nil {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing schema for %s: %s", name, result.Data)
			}
			result, _, err = callTool(ctx, discovery.NewDiscoverTool(discovery.NewResolver(registry)), toolshare.CallRequest{Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Content, "fallback rules") {
				t.Fatal("default workspace instructions repeated")
			}
		})
	}
}
