package execution

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool/builtin"
	toolmedia "elbot/internal/tool/media"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

func TestSendFileSourcePermissionsAndConfirmation(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	fileURI := (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}).String()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, t.TempDir())
	item, err := center.ImportBytes(ctx, []byte("media"), media.Input{Name: "media.txt", MIMEType: "text/plain"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	tool := builtin.NewSendFileTool(toolmedia.NewMediaRuntime(center, "", mediatest.Publisher(center)))
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	userCtx := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleUser})
	available, err := registry.QueryTools(userCtx, share.ToolQuery{Names: []string{"send_file"}})
	if err != nil || len(available) != 1 {
		t.Fatalf("regular user discovery = %v, err = %v", available, err)
	}

	for _, tc := range []struct {
		name        string
		source      string
		role        contextinfo.Role
		userMaxRisk string
		confirmRisk string
		reject      bool
		wantConfirm bool
		wantError   string
	}{
		{name: "user HTTP", source: "http://example.com/report.txt", role: contextinfo.RoleUser},
		{name: "user HTTPS", source: "https://example.com/report.txt", role: contextinfo.RoleUser},
		{name: "user media ID with local storage", source: item.ID, role: contextinfo.RoleUser},
		{name: "superadmin URL", source: "https://example.com/report.txt", role: contextinfo.RoleSuperadmin},
		{name: "superadmin media ID", source: item.ID, role: contextinfo.RoleSuperadmin},
		{name: "user relative path", source: "report.txt", role: contextinfo.RoleUser, wantError: "发送本地文件需要超级管理员权限"},
		{name: "user absolute path", source: path, role: contextinfo.RoleUser, wantError: "发送本地文件需要超级管理员权限"},
		{name: "user file URI", source: fileURI, role: contextinfo.RoleUser, wantError: "发送本地文件需要超级管理员权限"},
		{name: "user high risk allowance", source: path, role: contextinfo.RoleUser, userMaxRisk: "critical", wantError: "发送本地文件需要超级管理员权限"},
		{name: "missing actor before filesystem access", source: "missing.txt", wantError: "发送本地文件需要超级管理员权限"},
		{name: "superadmin relative path", source: "report.txt", role: contextinfo.RoleSuperadmin, wantConfirm: true},
		{name: "superadmin absolute path", source: path, role: contextinfo.RoleSuperadmin, wantConfirm: true},
		{name: "superadmin file URI", source: fileURI, role: contextinfo.RoleSuperadmin, wantConfirm: true},
		{name: "local confirmation rejected", source: path, role: contextinfo.RoleSuperadmin, reject: true, wantConfirm: true, wantError: "rejected"},
		{name: "local custom confirmation threshold", source: path, role: contextinfo.RoleSuperadmin, confirmRisk: "critical"},
		{name: "URL custom confirmation threshold", source: "https://example.com/report.txt", role: contextinfo.RoleSuperadmin, confirmRisk: "low", wantConfirm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := contextinfo.Actor{Role: tc.role}
			callCtx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: dir})
			if tc.role != "" {
				callCtx = contextinfo.WithActor(callCtx, actor)
			}
			permission := security.NewPermission(tc.userMaxRisk, tc.confirmRisk, nil)
			manager := newTestManager(registry, permission, center)
			args, err := json.Marshal(map[string]string{"source": tc.source})
			if err != nil {
				t.Fatal(err)
			}
			deps := &runnerTestDeps{reject: tc.reject}
			result := manager.Run(callCtx, deps, share.RunRequest{
				Committer: &testToolCommitter{},
				Session:   &storage.Session{ID: "send-file", Mode: storage.SessionModeWork},
				Actor:     actor,
				Calls:     []llm.ToolCallRequest{{ID: "send-file-call", Name: "send_file", Arguments: string(args)}},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if deps.confirmed != tc.wantConfirm {
				t.Fatalf("confirmed = %v, want %v", deps.confirmed, tc.wantConfirm)
			}
			if len(result.Messages) != 1 {
				t.Fatalf("messages = %#v", result.Messages)
			}
			content := llm.SegmentsContentText(result.Messages[0].Segments)
			if tc.wantError != "" {
				if deps.completed != 0 || !strings.Contains(content, tc.wantError) {
					t.Fatalf("completed = %d, content = %q, want error %q", deps.completed, content, tc.wantError)
				}
			} else if deps.completed != 1 || !strings.HasPrefix(content, "已发送文件：") {
				t.Fatalf("completed = %d, content = %q", deps.completed, content)
			}
		})
	}
}

func TestSendFileDirectCallRejectsLocalForRegularUser(t *testing.T) {
	permission := security.NewPermission("critical", "high", nil)
	registry := toolregistry.NewRegistry(permission)
	if err := registry.Register(builtin.NewSendFileTool(nil)); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, permission)
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
	_, _, err := manager.CallTool(ctx, share.CallRequest{Name: "send_file", Arguments: json.RawMessage(`{"source":"missing.txt"}`)})
	if err == nil || !strings.Contains(err.Error(), "发送本地文件需要超级管理员权限") {
		t.Fatalf("direct local call error = %v", err)
	}
}
