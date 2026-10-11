package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm/chatcompletions"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
)

type takeoverPermissionTool struct{ slowTool }

func (t takeoverPermissionTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
	before, _ := contextinfo.ActorFromContext(ctx)
	result, body, err := t.slowTool.Call(ctx, req)
	if err != nil {
		return result, body, err
	}
	after, _ := contextinfo.ActorFromContext(ctx)
	if before != after || after.Role != contextinfo.RoleSuperadmin || !sandboxctx.BackgroundContext(ctx) || ctx.Err() != nil {
		return nil, "permissions changed", fmt.Errorf("running permissions changed: before=%+v after=%+v", before, after)
	}
	result.Outputs = []delivery.Output{delivery.Text("tool output after takeover")}
	return result, body, nil
}

func TestTakeoverKeepsActivePermissionsButRoutesItsOutputToForeground(t *testing.T) {
	p := &fakePlatform{}
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan context.Context, 1)
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{Index: 0, ID: "slow", Name: "slow", Args: "{}"}, {Index: 1, ID: "next", Name: "foreground_probe", Args: "{}"}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	registry := tool.NewRegistry()
	if err := registry.Register(takeoverPermissionTool{slowTool{started, release}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(foregroundContextProbe{observed}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
		cfg.SandboxRoot = t.TempDir()
	})
	a.output.dispatcher.RegisterPlatformSender("qq", p)
	connectFixtureNotifications(t, "qq", a.output.dispatcher)
	done := startTakeoverTest(a)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	id := f.chatRequests()[0].SessionID
	if err := a.HandleMessage(takeoverPrivateContext(), "/resume "+id); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	result := awaitTakeover(t, done)
	if !result.TakenOver || result.Outcome != "completed" || !strings.Contains(p.out.String(), "tool output after takeover") {
		t.Fatalf("active call failed or output was not routed: %+v, %s", result, p.out.String())
	}
	select {
	case ctx := <-observed:
		actor, _ := contextinfo.ActorFromContext(ctx)
		if actor.Role != contextinfo.RoleUser || sandboxctx.BackgroundContext(ctx) {
			t.Fatal("next call retained background permissions", actor)
		}
	default:
		t.Fatal("next foreground tool did not execute")
	}
}
