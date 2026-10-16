package tool

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
)

func TestPreparedExecutionBindsRequestAndChecksAuthority(t *testing.T) {
	for _, scenario := range []string{"success", "arguments", "id", "name", "risk", "removed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := fakeTool{name: "prepared", source: SourceBuiltin, risk: RiskHigh}
			registry := NewRegistry()
			if err := registry.Register(candidate); err != nil {
				t.Fatal(err)
			}
			req := CallRequest{ID: "one", Name: candidate.Name(), Arguments: []byte(`{"value":1}`)}
			prepared := NewPreparedCall(candidate, req, "approved detail", func(ctx context.Context) (*Result, string, error) {
				if scenario != "success" {
					t.Fatal("rejected or canceled call entered business operation")
				}
				return &Result{Content: "approved result"}, "approved log", nil
			})
			// External mutation must not rewrite the approved request identity.
			req.Arguments[9] = '2'
			call := llm.ToolCallRequest{ID: "one", Name: candidate.Name(), Arguments: `{"value":1}`}
			executor := Executor{Registry: registry, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Policy: security.DefaultPolicy()}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "arguments":
				call.Arguments = `{"value":2}`
			case "id":
				call.ID = "two"
			case "name":
				call.Name = "other"
				if err := registry.Register(fakeTool{name: "other", source: SourceBuiltin}); err != nil {
					t.Fatal(err)
				}
			case "risk":
				executor.Actor.Role = contextinfo.RoleUser
			case "removed":
				executor.Registry = NewRegistry()
			case "canceled":
				cancel()
			}
			result := executor.ExecutePrepared(ctx, call, prepared)
			if scenario == "success" {
				if result.Error != nil || !result.ExecutionStarted || result.Result.Content != "approved result" || result.LogResult.Content != "approved log" {
					t.Fatalf("execution = %+v", result)
				}
			} else if result.Error == nil {
				t.Fatalf("expected refusal: %+v", result)
			}
			if scenario == "risk" && result.ResultStatus != events.ResultRejected {
				t.Fatalf("policy = %+v", result)
			}
			if scenario == "canceled" && !errors.Is(result.Error, context.Canceled) {
				t.Fatalf("cancel = %v", result.Error)
			}
		})
	}
}

func TestOrdinaryToolUsesCurrentRegistration(t *testing.T) {
	registry := NewRegistry()
	first := fakeTool{name: "dynamic", source: SourceSkillGo, risk: RiskLow}
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCallRequest{ID: "one", Name: first.Name(), Arguments: `{}`}
	prepared, err := PrepareCall(t.Context(), first, CallRequest{ID: call.ID, Name: call.Name, Arguments: []byte(call.Arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceSources([]Source{SourceSkillGo}, []Tool{fakeTool{name: "dynamic", source: SourceSkillGo, risk: RiskCritical}}); err != nil {
		t.Fatal(err)
	}
	result := (Executor{Registry: registry, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Policy: security.DefaultPolicy()}).ExecutePrepared(t.Context(), call, prepared)
	if result.ResultStatus != events.ResultRejected || result.ExecutionStarted {
		t.Fatalf("current policy ignored: %+v", result)
	}
}
