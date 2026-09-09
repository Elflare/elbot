package toolrun

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

type mediaArgumentRecorder struct{ received string }

func (t *mediaArgumentRecorder) Name() string { return "record_media" }
func (t *mediaArgumentRecorder) Info() tool.Info {
	return tool.Info{Name: t.Name(), Risk: tool.RiskLow}
}
func (t *mediaArgumentRecorder) Schema() llm.ToolSchema {
	return tool.NewBuilder(t.Name()).BuildSchema()
}
func (t *mediaArgumentRecorder) Call(_ context.Context, req tool.CallRequest) (*tool.Result, error) {
	t.received = string(req.Arguments)
	return &tool.Result{Content: "ok"}, nil
}
func TestMediaArgumentsRemainExplicit(t *testing.T) {
	target := &mediaArgumentRecorder{}
	registry := tool.NewRegistry()
	if err := registry.Register(target); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, nil)
	arguments := `{ "source":"media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload":{"text":"unchanged"} }`
	if !json.Valid([]byte(arguments)) {
		t.Fatal("bad fixture")
	}
	call := llm.ToolCallRequest{Name: target.Name(), Arguments: arguments}
	resolved := ResolvedTool{Name: target.Name(), Available: true, Native: target}
	if _, err := manager.AssessRisk(context.Background(), resolved, arguments); err != nil {
		t.Fatal(err)
	}
	result := manager.Execute(context.Background(), call, resolved, security.Actor{Role: security.RoleSuperadmin})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if target.received != arguments || result.Call.Arguments != arguments {
		t.Fatalf("rewritten call: %#v, %s", result.Call, target.received)
	}
}
