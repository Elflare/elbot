package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/state"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"

	"elbot/internal/workspace"
)

type rollbackWorkspace struct{ dir string }

func (s *rollbackWorkspace) GetWorkspaceDir(context.Context) (string, error) { return s.dir, nil }
func (s *rollbackWorkspace) SetWorkspaceDir(_ context.Context, dir string) error {
	s.dir = dir
	return nil
}
func (s *rollbackWorkspace) ClearWorkspaceDir(context.Context) error { s.dir = ""; return nil }

func (s *rollbackWorkspace) ClearWorkspaceDirIfMatches(_ context.Context, dir string) (bool, error) {
	if s.dir != dir {
		return false, nil
	}
	s.dir = ""
	return true, nil
}

type runnerTestDeps struct {
	confirmed      bool
	recorded       []storage.ToolCallRecord
	prepareContext func(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
}

func (*runnerTestDeps) RefreshExecution(ctx context.Context, _ *storage.Session) (context.Context, error) {
	return ctx, nil
}
func (*runnerTestDeps) Begin(context.Context, *storage.Message) error {
	return nil
}
func (*runnerTestDeps) Prepared(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (d *runnerTestDeps) Result(_ context.Context, _ int, _ llm.ToolCallRequest, _ llm.LLMMessage, update *storage.ToolResultCommit) error {
	d.recorded = append(d.recorded, *update.Record)
	return nil
}

func (*runnerTestDeps) Flush(context.Context) error {
	return nil
}

func (d *runnerTestDeps) PrepareToolCall(_ context.Context, _ *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	return call, nil
}
func (d *runnerTestDeps) ShouldSendPreview(context.Context, string) bool {
	return false
}
func (d *runnerTestDeps) StartToolRequest(ctx context.Context, _, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (d *runnerTestDeps) PrepareToolContext(ctx context.Context, s *storage.Session, c llm.ToolCallRequest) context.Context {
	return d.prepareContext(ctx, s, c)
}
func (d *runnerTestDeps) CompleteToolCall(_ context.Context, _ *storage.Session, _ llm.ToolCallRequest, _ string, result operationresult.Result[[]llm.MessageSegment], callErr error) ([]llm.MessageSegment, error) {
	if callErr != nil {
		return llm.TextSegments(callErr.Error()), nil
	}
	if result.Status != operationresult.Success {
		return llm.TextSegments(result.Message), nil
	}
	return result.Data, nil
}
func (d *runnerTestDeps) SendPreview(context.Context, ...string) {}
func (d *runnerTestDeps) SendOutputs(context.Context, []delivery.Output) (operationresult.Result[struct{}], error) {
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (d *runnerTestDeps) PublishToolResult(context.Context, string, delivery.ToolResult) {}
func (d *runnerTestDeps) AuditToolDenied(context.Context, string, llm.ToolCallRequest, toolshare.RiskLevel, string) {
}
func (d *runnerTestDeps) AddToolUse(string, string) {}
func (d *runnerTestDeps) ToolResultMessage(id string, message llm.LLMMessage) storage.Message {
	return storage.Message{ID: storage.NewID(), SessionID: id, Role: storage.RoleTool, Content: llm.SegmentsContentText(message.Segments)}
}
func (d *runnerTestDeps) ToolCallMessage(id, content, _, _ string, _ []llm.ToolCallRequest) storage.Message {
	return storage.Message{ID: storage.NewID(), SessionID: id, Role: storage.RoleAssistant, Content: content}
}

type rollbackConfirmDeps struct {
	runnerTestDeps
	detail             []toolshare.ConfirmationDetailBlock
	duringConfirmation func()
}

func (d *rollbackConfirmDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk toolshare.RiskAssessment, detail []toolshare.ConfirmationDetailBlock) (operationresult.Result[toolshare.ConfirmResult], error) {
	d.confirmed = true
	d.detail = detail
	if d.duringConfirmation != nil {
		d.duringConfirmation()
	}
	return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Success}, nil
}

func rollbackConfirmationText(details []toolshare.ConfirmationDetailBlock) string {
	var text strings.Builder
	for _, block := range details {
		text.WriteString(fmt.Sprint(block.Content))
	}
	return text.String()
}

func TestRunRollbackPreservesPreflightThroughConfirmation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			service := fileops.NewService(nil)
			binding := runnerRollbackBinding{}
			ctx := service.WithBinding(context.Background(), binding)
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			edit := func(before, after string) {
				t.Helper()
				result, _, err := service.Edit(ctx, fileops.EditRequest{Path: path, ExpectedRevision: fileops.ContentRevision([]byte(before)), Edits: []fileops.Edit{{Operation: "overwrite", NewText: &after}}})
				if err != nil || result.Status != operationresult.Success {
					t.Fatalf("edit: %+v %v", result, err)
				}
			}
			edit("before", "after")
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(RollbackFileTool{Service: service}).Definition()}); err != nil {
				t.Fatal(err)
			}
			manager := execution.NewManager(execution.ManagerDeps{Registry: registry, Permission: security.NewPermission("low", "high", nil), InfoAvailableInContext: toolregistry.InfoAvailableInContext, UnavailableReason: toolregistry.UnavailableReason, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools, DecodeState: state.DecodeState, PrepareUpdate: state.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(registry).DiscoveryStateUpdate, Log: func(ctx context.Context, record events.LogRecord) {
				events.Log(context.WithoutCancel(ctx), record)
			}})
			actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}}
			base := contextinfo.WithActor(context.Background(), actor)
			deps := &rollbackConfirmDeps{}
			deps.prepareContext = func(ctx context.Context, _ *storage.Session, _ llm.ToolCallRequest) context.Context {
				ctx = workspace.WithWorkspaceStore(ctx, &rollbackWorkspace{dir: dir})
				return service.WithBinding(ctx, binding)
			}
			if changed {
				deps.duringConfirmation = func() { edit("after", "latest") }
			}
			result := manager.Run(base, deps, toolshare.RunRequest{Committer: deps,
				Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork},
				Actor:   actor,
				Calls:   []llm.ToolCallRequest{{ID: "rollback", Name: "rollback_file", Arguments: `{"path":"file"}`}},
			})
			if !deps.confirmed || !strings.Contains(rollbackConfirmationText(deps.detail), path) || !strings.Contains(rollbackConfirmationText(deps.detail), "--- ") {
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
			if len(deps.recorded) != 1 || (deps.recorded[0].Error != "") != changed {
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
			editor := (EditFileTool{Rollback: service})
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{editor.Definition()}); err != nil {
				t.Fatal(err)
			}
			manager := execution.NewManager(execution.ManagerDeps{Registry: registry, Permission: security.NewPermission("low", "high", nil), InfoAvailableInContext: toolregistry.InfoAvailableInContext, UnavailableReason: toolregistry.UnavailableReason, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools, DecodeState: state.DecodeState, PrepareUpdate: state.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(registry).DiscoveryStateUpdate, Log: func(ctx context.Context, record events.LogRecord) {
				events.Log(context.WithoutCancel(ctx), record)
			}})
			actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}}
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
			manager.Run(contextinfo.WithActor(context.Background(), actor), deps, toolshare.RunRequest{Committer: deps,
				Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork}, Actor: actor,
				Calls: []llm.ToolCallRequest{{ID: "edit", Name: "edit_file", Arguments: `{"path":"file","edits":[{"operation":"replace_text","old_text":"before","new_text":"after"}]}`}},
			})
			if !deps.confirmed || !strings.Contains(rollbackConfirmationText(deps.detail), "-before") || !strings.Contains(rollbackConfirmationText(deps.detail), "+after") {
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
			if len(deps.recorded) != 1 || (deps.recorded[0].Error != "") != changed {
				t.Fatalf("recorded=%+v", deps.recorded)
			}
		})
	}
}
