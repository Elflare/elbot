package execution

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
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
			prepared := share.NewPreparedCall(req, share.RiskAssessment{Level: share.RiskHigh}, "approved detail", func(context.Context) (*share.Result, string, error) {
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
			result := (&Manager{}).executePrepared(ctx, call, resolvedTool{Source: share.SourceKindNative, ResultStatus: events.ResultSucceeded}, prepared, nil)
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
	prepare     func(context.Context, share.CallRequest) (*share.PreparedCall, error)
}

func (*snapshotTool) Name() string { return "snapshot" }
func (*snapshotTool) Info() share.Info {
	return share.Info{Name: "snapshot", Source: share.SourceSkillGo, Risk: share.RiskHigh}
}
func (*snapshotTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "snapshot"} }
func (s *snapshotTool) PrepareCall(ctx context.Context, req share.CallRequest) (*share.PreparedCall, error) {
	s.assessments++
	if s.prepare != nil {
		return s.prepare(ctx, req)
	}
	return share.NewPreparedCall(req, share.RiskAssessment{Level: share.RiskHigh}, "original snapshot", func(context.Context) (*share.Result, string, error) {
		s.calls++
		return &share.Result{Content: "original"}, "original", nil
	}), nil
}

func TestCallToolAlwaysPreparesAndChecksBeforeExecution(t *testing.T) {
	for _, scenario := range []string{"success", "prepare_error", "canceled_before", "canceled_after", "changed_request"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tool := &snapshotTool{}
			prepareErr := errors.New("prepare failed")
			if scenario != "success" {
				tool.prepare = func(_ context.Context, req share.CallRequest) (*share.PreparedCall, error) {
					if scenario == "prepare_error" {
						return nil, prepareErr
					}
					if scenario == "canceled_after" {
						cancel()
					}
					if scenario == "changed_request" {
						req.ID = "different"
					}
					return share.NewPreparedCall(req, share.RiskAssessment{Level: share.RiskHigh}, "snapshot", func(context.Context) (*share.Result, string, error) {
						tool.calls++
						return &share.Result{Content: "original"}, "original", nil
					}), nil
				}
			}
			catalog := registry.NewRegistry(security.DefaultPermission())
			if err := catalog.Register(tool); err != nil {
				t.Fatal(err)
			}
			if scenario == "canceled_before" {
				cancel()
			}
			result, logContent, err := (&Manager{ManagerDeps: ManagerDeps{Registry: catalog}}).CallTool(ctx, share.CallRequest{ID: "one", Name: tool.Name(), Arguments: []byte(`{}`)})
			if scenario == "success" {
				if err != nil || tool.assessments != 1 || tool.calls != 1 || result.Content != "original" || logContent != "original" {
					t.Fatalf("tool=%+v result=%+v log=%q error=%v", tool, result, logContent, err)
				}
				return
			}
			if err == nil || result != nil || tool.calls != 0 {
				t.Fatalf("invalid call executed: tool=%+v result=%+v error=%v", tool, result, err)
			}
			if scenario == "prepare_error" && !errors.Is(err, prepareErr) {
				t.Fatalf("prepare error lost: %v", err)
			}
			if scenario == "canceled_before" && tool.assessments != 0 {
				t.Fatal("canceled call was prepared")
			}
			if (scenario == "canceled_before" || scenario == "canceled_after") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
