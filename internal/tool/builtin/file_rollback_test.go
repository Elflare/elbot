package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	workspacepath "elbot/internal/workspace"
)

type rollbackFixture struct {
	binding   *rollbackTestBinding
	service   *fileops.Service
	edit      EditFileTool
	rollback  RollbackFileTool
	workspace *testWorkspaceStore
	base      context.Context
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	guard := NewFileGuard()
	service := fileops.NewService(guard.CheckWrite)
	binding := newRollbackTestBinding()
	workspace := &testWorkspaceStore{dir: t.TempDir()}
	base := workspacepath.WithWorkspaceStore(contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "admin", Role: contextinfo.RoleSuperadmin}), workspace)
	edit := NewEditFileTool(guard)
	edit.Rollback = service
	return &rollbackFixture{binding: binding, service: service, edit: edit, rollback: NewRollbackFileTool(service), workspace: workspace, base: base}
}

func (f *rollbackFixture) ctx() context.Context {
	return f.service.WithBinding(f.base, f.binding)
}

func (f *rollbackFixture) write(t *testing.T, path, text string) {
	t.Helper()
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(f.workspace.dir, path)
	}
	args := map[string]any{"path": path, "create": true, "edits": []map[string]any{{"operation": "overwrite", "new_text": text}}}
	if data, err := os.ReadFile(absolute); err == nil {
		args["expected_revision"] = fileops.ContentRevision(data)
	}
	raw, _ := json.Marshal(args)
	result, _, err := callTool(f.ctx(), f.edit, toolshare.CallRequest{Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "rollback_available: true") {
		t.Fatalf("result: %s", result.Content)
	}
}

func rollbackRequest(path string) toolshare.CallRequest {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return toolshare.CallRequest{ID: "rollback-call", Name: "rollback_file", Arguments: raw}
}

func TestRollbackToolPathOnlyAndWorkspace(t *testing.T) {
	f := newRollbackFixture(t)
	firstDir := f.workspace.dir
	f.write(t, "file", "original")
	f.write(t, "file", "edited")
	params := f.rollback.Schema().Parameters
	props := params["properties"].(map[string]any)
	if len(props) != 1 || props["path"] == nil || params["additionalProperties"] != false {
		t.Fatalf("schema: %#v", params)
	}
	for _, raw := range []string{`{}`, `{"path":"file","id":1}`, `{"path":"file","action":"rollback"}`} {
		if _, _, err := callTool(f.ctx(), f.rollback, toolshare.CallRequest{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	f.workspace.dir = t.TempDir()
	if _, _, err := callTool(f.ctx(), f.rollback, rollbackRequest("file")); err == nil {
		t.Fatal("relative path ignored current workspace")
	}
	result, _, err := callTool(f.ctx(), f.rollback, rollbackRequest(filepath.Join(firstDir, "file")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "action: restored") {
		t.Fatalf("result: %s", result.Content)
	}
	data, _ := os.ReadFile(filepath.Join(firstDir, "file"))
	if string(data) != "original" {
		t.Fatalf("data: %q", data)
	}
}

func TestRollbackToolPreflightPinsRecordAndWorkspace(t *testing.T) {
	f := newRollbackFixture(t)
	f.write(t, "file", "one")
	ctx := f.ctx()
	req := rollbackRequest("file")
	prepared, err := f.rollback.PrepareCall(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	detail := prepared.Preview()
	if !strings.Contains(detail, "删除本次新建文件") {
		t.Fatalf("detail: %s", detail)
	}
	f.write(t, "file", "two")
	derived := f.service.WithBinding(context.WithValue(ctx, struct{}{}, "request"), f.binding)
	if _, _, err := prepared.Call(derived); !errors.Is(err, fileops.ErrRollbackNotFound) {
		t.Fatalf("stale preflight: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(f.workspace.dir, "file"))
	if string(data) != "two" {
		t.Fatalf("data: %q", data)
	}
	ctx = f.ctx()
	prepared, err = f.rollback.PrepareCall(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	f.workspace.dir = t.TempDir()
	f.write(t, "file", "other")
	if _, _, err := prepared.Call(ctx); err == nil {
		t.Fatal("workspace switch silently changed target")
	}
}

func TestRollbackToolPermissionsAndSessionExpiry(t *testing.T) {
	f := newRollbackFixture(t)
	f.write(t, "file", "new")
	ctx := f.ctx()
	userCtx := contextinfo.WithActor(ctx, contextinfo.Actor{ID: "user", Role: contextinfo.RoleUser})
	if _, _, err := callTool(userCtx, f.rollback, rollbackRequest("file")); err == nil || !strings.Contains(err.Error(), "superadmin") {
		t.Fatalf("user: %v", err)
	}
	background := workspacepath.WithPathScope(contextinfo.WithBackgroundKind(ctx, ""), workspacepath.PathScope{Dir: f.workspace.dir, Restricted: true})
	if _, _, err := callTool(background, f.rollback, rollbackRequest("file")); err == nil {
		t.Fatal("background rollback allowed")
	}
	if session, err := f.service.EditSession(background); err != nil || session != nil {
		t.Fatalf("background recording: %v %v", session, err)
	}
	f.binding.valid.Store(false)
	f.binding = newRollbackTestBinding()
	if _, _, err := callTool(f.service.WithBinding(ctx, f.binding), f.rollback, rollbackRequest("file")); !errors.Is(err, fileops.ErrRollbackExpired) {
		t.Fatalf("lease revived: %v", err)
	}
	if _, _, err := callTool(f.ctx(), f.rollback, rollbackRequest("file")); !errors.Is(err, fileops.ErrRollbackNotFound) {
		t.Fatalf("record revived: %v", err)
	}
}

func TestRollbackToolFileGuardChecksActualTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	f := newRollbackFixture(t)
	target := filepath.Join(f.workspace.dir, "real")
	link := filepath.Join(f.workspace.dir, "link")
	f.write(t, target, "one")
	f.write(t, target, "two")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	guard := NewFileGuard(FileGuardRule{ExactPath: target, WriteError: "protected target"})
	f.service.CheckWrite = guard.CheckWrite
	if _, _, err := callTool(f.ctx(), f.rollback, rollbackRequest(link)); err == nil || !strings.Contains(err.Error(), "protected target") {
		t.Fatalf("guard: %v", err)
	}
	f.edit.FileGuard = guard
	raw, _ := json.Marshal(map[string]any{"path": link, "expected_revision": fileops.ContentRevision([]byte("two")), "edits": []map[string]any{{"operation": "overwrite", "new_text": "three"}}})
	if _, _, err := callTool(f.ctx(), f.edit, toolshare.CallRequest{Arguments: raw}); err == nil || !strings.Contains(err.Error(), "protected target") {
		t.Fatalf("edit guard: %v", err)
	}
	records, _ := f.service.List(f.ctx())
	if len(records) != 1 {
		t.Fatal("denial consumed record")
	}
}

func TestRollbackToolHiddenDependencyDiscovery(t *testing.T) {
	registry := toolregistry.NewRegistry(security.NewPermission("critical", "critical", nil))
	for _, item := range []toolshare.Tool{NewWorkspaceTool(), NewReadFileTool(), NewEditFileTool(), NewRollbackFileTool(fileops.NewService(nil))} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	rollback, ok := registry.Get("rollback_file")
	if !ok {
		t.Fatal("missing rollback tool")
	}
	info := rollback.Info()
	if !info.Hidden || !info.SuperadminOnly || !info.ForegroundOnly || info.Risk != toolshare.RiskHigh || !slices.Equal(info.Tags, []string{"files"}) {
		t.Fatalf("info: %+v", info)
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "admin", Role: contextinfo.RoleSuperadmin})
	resolver := discovery.NewResolver(registry)
	list, err := resolver.Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range list.Tools {
		if entry.Info.Name == "rollback_file" {
			t.Fatal("hidden tool listed")
		}
	}
	hidden, err := resolver.Discover(ctx, []string{"rollback_file"})
	if err != nil || len(hidden.Errors) != 1 || len(hidden.Tools) != 0 {
		t.Fatalf("hidden discovery = %+v, %v", hidden, err)
	}
	for _, name := range []string{"read_file", "edit_file"} {
		result, err := resolver.Discover(ctx, []string{name})
		if err != nil || len(result.Errors) != 0 {
			t.Fatalf("discover = %+v, %v", result, err)
		}
		found := false
		for _, entry := range result.Tools {
			if entry.Info.Name == "rollback_file" && entry.Schema != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s did not expand rollback", name)
		}
	}
	details, err := resolver.DiscoverDetails(ctx, []string{"read_file", "edit_file"})
	if err != nil || len(details.Errors) != 0 {
		t.Fatalf("details = %+v, %v", details, err)
	}
	count := 0
	for _, entry := range details.Tools {
		if entry.Info.Name == "rollback_file" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate dependencies: %d", count)
	}
	userCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "user", Role: contextinfo.RoleUser})
	details, err = resolver.DiscoverDetails(userCtx, []string{"edit_file"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range details.Tools {
		if entry.Info.Name == "rollback_file" {
			t.Fatal("user discovered superadmin dependency")
		}
	}
}

type rollbackTestBinding struct{ valid atomic.Bool }

func (b *rollbackTestBinding) Valid() bool { return b.valid.Load() }
func newRollbackTestBinding() *rollbackTestBinding {
	b := &rollbackTestBinding{}
	b.valid.Store(true)
	return b
}
