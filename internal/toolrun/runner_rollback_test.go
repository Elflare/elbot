package toolrun_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/toolrun"
	"elbot/internal/workspace"
)

type rollbackWorkspace struct{ dir string }

func (s *rollbackWorkspace) GetWorkspaceDir(context.Context) (string, error) { return s.dir, nil }
func (s *rollbackWorkspace) SetWorkspaceDir(_ context.Context, dir string) error {
	s.dir = dir
	return nil
}
func (s *rollbackWorkspace) ClearWorkspaceDir(context.Context) error { s.dir = ""; return nil }

type runnerTestDeps struct {
	confirmed      bool
	recorded       []struct{ err error }
	prepareContext func(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
}

func (d *runnerTestDeps) PrepareToolCall(_ context.Context, _ *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	return call, nil
}
func (d *runnerTestDeps) ShouldSendPreview(context.Context, *storage.Session, llm.ToolCallRequest, string) bool {
	return false
}
func (d *runnerTestDeps) ConfirmBackgroundTool(context.Context, string, llm.ToolCallRequest, toolrun.ResolvedTool, tool.RiskAssessment) (toolrun.ConfirmResult, bool) {
	return toolrun.ConfirmResult{}, false
}
func (d *runnerTestDeps) StartToolRequest(ctx context.Context, _, _ string) (context.Context, time.Time, func(), error) {
	return ctx, time.Now(), func() {}, nil
}
func (d *runnerTestDeps) PrepareToolContext(ctx context.Context, s *storage.Session, c llm.ToolCallRequest) context.Context {
	return d.prepareContext(ctx, s, c)
}
func (d *runnerTestDeps) CompleteToolCall(_ context.Context, _ *storage.Session, _ llm.ToolCallRequest, _ string, segments []llm.MessageSegment, _ error) ([]llm.MessageSegment, error) {
	return segments, nil
}
func (d *runnerTestDeps) SendPreview(context.Context, string)                  {}
func (d *runnerTestDeps) SendOutputs(context.Context, []delivery.Output) error { return nil }
func (d *runnerTestDeps) RecordToolCall(_ context.Context, _ string, _ llm.ToolCallRequest, _ string, _ time.Time, _ string, err error) {
	d.recorded = append(d.recorded, struct{ err error }{err})
}
func (d *runnerTestDeps) AuditToolDenied(context.Context, string, llm.ToolCallRequest, tool.RiskLevel, string) {
}
func (d *runnerTestDeps) RememberDiscoveryResult(context.Context, *storage.Session, *tool.Result) {}
func (d *runnerTestDeps) AddToolUse(string, string)                                               {}
func (d *runnerTestDeps) ToolResultMessage(id string, message llm.LLMMessage) storage.Message {
	return storage.Message{SessionID: id, Role: storage.RoleTool, Content: llm.SegmentsContentText(message.Segments)}
}
func (d *runnerTestDeps) ToolCallMessage(id, content, _ string, _ []llm.ToolCallRequest) storage.Message {
	return storage.Message{SessionID: id, Role: storage.RoleAssistant, Content: content}
}
func (d *runnerTestDeps) PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage { return message }

type rollbackConfirmDeps struct {
	runnerTestDeps
	detail             string
	duringConfirmation func()
}

func (d *rollbackConfirmDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskAssessment, detail string) (toolrun.ConfirmResult, error) {
	d.confirmed = true
	d.detail = detail
	if d.duringConfirmation != nil {
		d.duringConfirmation()
	}
	return toolrun.ConfirmResult{Allowed: true}, nil
}

func TestRunRollbackPreservesPreflightThroughConfirmation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			service := fileops.NewService(nil)
			binding := runnerRollbackBinding{}
			lease, _ := service.Manager.Session(binding)
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			edit := func(before, after string) {
				t.Helper()
				_, err := lease.EditFile(context.Background(), path, "", fileops.ContentRevision([]byte(before)), false, 3, []fileops.Edit{{Operation: "overwrite", NewText: &after}}, fileops.EditFileOptions{}, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			edit("before", "after")
			registry := tool.NewRegistry()
			if err := registry.Register(builtin.NewRollbackFileTool(service)); err != nil {
				t.Fatal(err)
			}
			manager := toolrun.NewManager(registry, security.NewPolicy("low", "high", nil))
			actor := security.Actor{ID: "admin", Role: security.RoleSuperadmin}
			base := security.WithActor(context.Background(), actor)
			deps := &rollbackConfirmDeps{}
			deps.prepareContext = func(ctx context.Context, _ *storage.Session, _ llm.ToolCallRequest) context.Context {
				ctx = workspace.WithWorkspaceStore(ctx, &rollbackWorkspace{dir: dir})
				return service.WithBinding(ctx, binding)
			}
			if changed {
				deps.duringConfirmation = func() { edit("after", "latest") }
			}
			result := manager.Run(base, deps, toolrun.RunRequest{
				Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork},
				Actor:   actor,
				Calls:   []llm.ToolCallRequest{{ID: "rollback", Name: "rollback_file", Arguments: `{"path":"file"}`}},
			})
			if !deps.confirmed || !strings.Contains(deps.detail, path) || !strings.Contains(deps.detail, "恢复编辑前内容") {
				t.Fatalf("confirmation lost context: %q", deps.detail)
			}
			data, _ := os.ReadFile(path)
			want := "before"
			if changed {
				want = "latest"
			}
			if string(data) != want {
				t.Fatalf("data=%q want=%q", data, want)
			}
			if len(deps.recorded) != 1 || (deps.recorded[0].err != nil) != changed {
				t.Fatalf("recorded=%+v result=%+v", deps.recorded, result)
			}
		})
	}
}

type runnerRollbackBinding struct{}

func (runnerRollbackBinding) Valid() bool { return true }

func TestRunEditPreservesConfirmationSnapshot(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			service := fileops.NewService(nil)
			binding := runnerRollbackBinding{}
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			editor := builtin.NewEditFileTool()
			editor.Rollback = service
			registry := tool.NewRegistry()
			if err := registry.Register(editor); err != nil {
				t.Fatal(err)
			}
			manager := toolrun.NewManager(registry, security.NewPolicy("low", "high", nil))
			actor := security.Actor{ID: "admin", Role: security.RoleSuperadmin}
			deps := &rollbackConfirmDeps{}
			deps.prepareContext = func(ctx context.Context, _ *storage.Session, _ llm.ToolCallRequest) context.Context {
				ctx = workspace.WithWorkspaceStore(ctx, &rollbackWorkspace{dir: dir})
				return service.WithBinding(ctx, binding)
			}
			if changed {
				deps.duringConfirmation = func() {
					if err := os.WriteFile(path, []byte("before changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			manager.Run(security.WithActor(context.Background(), actor), deps, toolrun.RunRequest{
				Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork}, Actor: actor,
				Calls: []llm.ToolCallRequest{{ID: "edit", Name: "edit_file", Arguments: `{"path":"file","edits":[{"operation":"replace_text","old_text":"before","new_text":"after"}]}`}},
			})
			if !deps.confirmed || !strings.Contains(deps.detail, "-before") || !strings.Contains(deps.detail, "+after") {
				t.Fatalf("confirmation=%q", deps.detail)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "after"
			if changed {
				want = "before changed"
			}
			if string(data) != want {
				t.Fatalf("file=%q want=%q", data, want)
			}
			if len(deps.recorded) != 1 || (deps.recorded[0].err != nil) != changed {
				t.Fatalf("recorded=%+v", deps.recorded)
			}
		})
	}
}
