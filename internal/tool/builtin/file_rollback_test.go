package builtin

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

	"elbot/internal/fileops"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/tool"
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
	base := workspacepath.WithWorkspaceStore(security.WithActor(context.Background(), security.Actor{ID: "admin", Role: security.RoleSuperadmin}), workspace)
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
	result, err := f.edit.Call(f.ctx(), tool.CallRequest{Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "rollback_available: true") {
		t.Fatalf("result: %s", result.Content)
	}
}

func rollbackRequest(path string) tool.CallRequest {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return tool.CallRequest{ID: "rollback-call", Name: "rollback_file", Arguments: raw}
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
		if _, err := f.rollback.Call(f.ctx(), tool.CallRequest{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	f.workspace.dir = t.TempDir()
	if _, err := f.rollback.Call(f.ctx(), rollbackRequest("file")); err == nil {
		t.Fatal("relative path ignored current workspace")
	}
	result, err := f.rollback.Call(f.ctx(), rollbackRequest(filepath.Join(firstDir, "file")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "action: restored") || strings.Contains(result.Content, "id:") {
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
	if err := f.rollback.PreflightConfirmation(ctx, req); err != nil {
		t.Fatal(err)
	}
	detail, err := f.rollback.RiskDetail(ctx, req)
	if err != nil || !strings.Contains(detail, "删除本次新建文件") {
		t.Fatalf("detail: %s %v", detail, err)
	}
	f.write(t, "file", "two")
	derived := f.service.WithBinding(context.WithValue(ctx, struct{}{}, "request"), f.binding)
	if _, err := f.rollback.Call(derived, req); !errors.Is(err, fileops.ErrRollbackNotFound) {
		t.Fatalf("stale preflight: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(f.workspace.dir, "file"))
	if string(data) != "two" {
		t.Fatalf("data: %q", data)
	}
	ctx = f.ctx()
	if err := f.rollback.PreflightConfirmation(ctx, req); err != nil {
		t.Fatal(err)
	}
	f.workspace.dir = t.TempDir()
	f.write(t, "file", "other")
	if _, err := f.rollback.Call(ctx, req); err == nil {
		t.Fatal("workspace switch silently changed target")
	}
}

func TestRollbackToolPermissionsAndSessionExpiry(t *testing.T) {
	f := newRollbackFixture(t)
	f.write(t, "file", "new")
	ctx := f.ctx()
	userCtx := security.WithActor(ctx, security.Actor{ID: "user", Role: security.RoleUser})
	if _, err := f.rollback.Call(userCtx, rollbackRequest("file")); err == nil || !strings.Contains(err.Error(), "superadmin") {
		t.Fatalf("user: %v", err)
	}
	background := sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Background: true, Dir: f.workspace.dir})
	if _, err := f.rollback.Call(background, rollbackRequest("file")); err == nil {
		t.Fatal("background rollback allowed")
	}
	if session, err := f.service.EditSession(background); err != nil || session != nil {
		t.Fatalf("background recording: %v %v", session, err)
	}
	f.binding.valid.Store(false)
	f.binding = newRollbackTestBinding()
	if _, err := f.rollback.Call(f.service.WithBinding(ctx, f.binding), rollbackRequest("file")); !errors.Is(err, fileops.ErrRollbackExpired) {
		t.Fatalf("lease revived: %v", err)
	}
	if _, err := f.rollback.Call(f.ctx(), rollbackRequest("file")); !errors.Is(err, fileops.ErrRollbackNotFound) {
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
	if _, err := f.rollback.Call(f.ctx(), rollbackRequest(link)); err == nil || !strings.Contains(err.Error(), "protected target") {
		t.Fatalf("guard: %v", err)
	}
	f.edit.FileGuard = guard
	raw, _ := json.Marshal(map[string]any{"path": link, "expected_revision": fileops.ContentRevision([]byte("two")), "edits": []map[string]any{{"operation": "overwrite", "new_text": "three"}}})
	if _, err := f.edit.Call(f.ctx(), tool.CallRequest{Arguments: raw}); err == nil || !strings.Contains(err.Error(), "protected target") {
		t.Fatalf("edit guard: %v", err)
	}
	records, _ := f.service.List(f.ctx())
	if len(records) != 1 {
		t.Fatal("denial consumed record")
	}
}

func TestRollbackToolHiddenDependencyDiscovery(t *testing.T) {
	registry := tool.NewRegistry()
	if err := RegisterAll(registry, RegisterOptions{}); err != nil {
		t.Fatal(err)
	}
	rollback, ok := registry.Get("rollback_file")
	if !ok {
		t.Fatal("missing rollback tool")
	}
	info := rollback.Info()
	if !info.Hidden || !info.SuperadminOnly || !info.ForegroundOnly || info.Risk != tool.RiskHigh || !slices.Equal(info.Tags, []string{"files"}) {
		t.Fatalf("info: %+v", info)
	}
	list, err := registry.Discover("")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range list.Tools {
		if entry.Info.Name == "rollback_file" {
			t.Fatal("hidden tool listed")
		}
	}
	if _, err := registry.Discover("rollback_file"); err == nil {
		t.Fatal("hidden root discovery allowed")
	}
	for _, name := range []string{"read_file", "edit_file"} {
		result, err := registry.Discover(name)
		if err != nil {
			t.Fatal(err)
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
	ctx := security.WithActor(context.Background(), security.Actor{ID: "admin", Role: security.RoleSuperadmin})
	details, errs := registry.DiscoverDetails(ctx, []string{"read_file", "edit_file"}, func(candidate tool.Tool) bool { return tool.InfoAvailableInContext(ctx, candidate.Info()) })
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	count := 0
	for _, entry := range details {
		if entry.Info.Name == "rollback_file" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate dependencies: %d", count)
	}
	user := security.Actor{ID: "user", Role: security.RoleUser}
	details, _ = registry.DiscoverDetails(context.Background(), []string{"edit_file"}, func(candidate tool.Tool) bool {
		return tool.CanAccessTool(user, security.NewPolicy("critical", "high", nil), candidate.Info())
	})
	for _, entry := range details {
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
