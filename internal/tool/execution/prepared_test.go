package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"errors"
	"testing"
)

func TestPreparedExecutionBindsRequest(t *testing.T) {
	for _, scenario := range []string{"success", "arguments", "id", "name", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			req := share.CallRequest{ID: "one", Name: "prepared", Arguments: []byte(`{"value":1}`)}
			called := false
			prepared := share.NewPreparedCall(req, "approved detail", func(context.Context) (*share.Result, string, error) {
				called = true
				return &share.Result{Content: "approved result"}, "approved log", nil
			})
			req.Arguments[9] = '2'
			call := llm.ToolCallRequest{ID: "one", Name: "prepared", Arguments: `{"value":1}`}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "arguments":
				call.Arguments = `{"value":2}`
			case "id":
				call.ID = "two"
			case "name":
				call.Name = "other"
			case "canceled":
				cancel()
			}
			result := newTestManager(nil, nil).executePrepared(ctx, call, resolvedTool{Source: share.SourceKindNative, ResultStatus: events.ResultSucceeded}, prepared, nil)
			if scenario == "success" {
				if result.Error != nil || !called || !result.ExecutionStarted || result.Result.Content != "approved result" || result.LogResult.Content != "approved log" {
					t.Fatalf("result=%+v called=%v", result, called)
				}
			} else if result.Error == nil || called || result.ExecutionStarted {
				t.Fatalf("invalid call executed: %+v", result)
			}
			if scenario == "canceled" && !errors.Is(result.Error, context.Canceled) {
				t.Fatalf("cancel=%v", result.Error)
			}
		})
	}
}

type snapshotTool struct {
	assessments int
	calls       int
	prepared    bool
}

func (*snapshotTool) Name() string { return "snapshot" }
func (*snapshotTool) Info() share.Info {
	return share.Info{Name: "snapshot", Source: share.SourceSkillGo, Risk: share.RiskHigh}
}
func (*snapshotTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "snapshot"} }
func (s *snapshotTool) AssessRisk(context.Context, share.CallRequest) (share.RiskAssessment, error) {
	s.assessments++
	return share.RiskAssessment{Level: share.RiskHigh}, nil
}
func (s *snapshotTool) PrepareCall(_ context.Context, req share.CallRequest) (*share.PreparedCall, error) {
	if !s.prepared {
		return nil, nil
	}
	return share.NewPreparedCall(req, "original snapshot", func(ctx context.Context) (*share.Result, string, error) { return s.Call(ctx, req) }), nil
}
func (s *snapshotTool) Call(context.Context, share.CallRequest) (*share.Result, string, error) {
	s.calls++
	return &share.Result{Content: "original"}, "original", nil
}

type snapshotConfirmDeps struct {
	runnerTestDeps
	change func()
}

func (d *snapshotConfirmDeps) ConfirmToolCall(context.Context, string, llm.ToolCallRequest, share.RiskAssessment, string) (share.ConfirmResult, error) {
	d.confirmed = true
	d.change()
	return share.ConfirmResult{Allowed: true}, nil
}

func TestRunKeepsSelectedObjectAcrossCatalogChanges(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, action := range []string{"replace", "remove"} {
			name := action
			if prepared {
				name += "_prepared"
			}
			t.Run(name, func(t *testing.T) {
				permission := security.DefaultPermission()
				catalog := registry.NewRegistry(permission)
				original := &snapshotTool{prepared: prepared}
				replacement := &snapshotTool{}
				if err := catalog.Register(original); err != nil {
					t.Fatal(err)
				}
				deps := &snapshotConfirmDeps{change: func() {
					var err error
					if action == "replace" {
						err = catalog.ReplaceSources([]share.Source{share.SourceSkillGo}, []share.Tool{replacement})
					} else {
						err = catalog.Unregister(original.Name())
					}
					if err != nil {
						t.Fatal(err)
					}
				}}
				manager := newTestManager(catalog, permission)
				req := share.RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "snapshot", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "one", Name: original.Name(), Arguments: "{}"}}}
				result := manager.Run(t.Context(), deps, req)
				if result.Err != nil || !deps.confirmed || original.calls != 1 || original.assessments != 1 || replacement.calls != 0 {
					t.Fatalf("result=%+v old=%+v new=%+v", result, original, replacement)
				}
				req.Committer = &testToolCommitter{}
				result = manager.Run(t.Context(), &runnerTestDeps{}, req)
				if result.Err != nil || original.calls != 1 || original.assessments != 1 {
					t.Fatalf("old object reused: %+v", original)
				}
				if action == "replace" && (replacement.calls != 1 || replacement.assessments != 1) {
					t.Fatalf("new object not selected: %+v", replacement)
				}
				if action == "remove" && (len(result.Messages) != 1 || llm.SegmentsContentText(result.Messages[0].Segments) == "original") {
					t.Fatalf("removed object still callable: %+v", result)
				}
			})
		}
	}
}
