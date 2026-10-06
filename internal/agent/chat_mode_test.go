package agent

import (
	"context"
	"elbot/internal/llm/chatcompletions"
	"testing"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func TestChatModeIgnoresForcedHookToolsAndModelCalls(t *testing.T) {
	t.Run("foreground", func(t *testing.T) {
		ctx := context.Background()
		var executed string
		candidate := preparedArgumentTool{arguments: &executed}
		registry := tool.NewRegistry()
		if err := registry.Register(candidate); err != nil {
			t.Fatal(err)
		}
		f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
			{{DeltaContent: "plain answer", ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "model-call", Name: candidate.Name(), Args: "{}"}}}},
			{{DeltaContent: "unexpected followup"}},
		}}

		hooks := hook.NewManager()
		for _, point := range []hook.Point{hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared, hook.PointLLMResponseReceived} {
			if err := hooks.Register(hook.Registration{Point: point, Name: "force-tools", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
				if event.Point == hook.PointLLMResponseReceived {
					event.LLM.ToolCalls = append(event.LLM.ToolCalls, llm.ToolCallRequest{ID: "hook-call", Name: candidate.Name(), Arguments: "{}"})
				} else {
					event.LLM.Tools = []llm.ToolSchema{candidate.Schema()}
				}
				return event, nil
			})}); err != nil {
				t.Fatal(err)
			}
		}
		a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
			cfg.SandboxRoot = t.TempDir()
			cfg.ToolRegistry = registry
			cfg.HookManager = hooks
		})
		var id string
		{
			row, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Mode: storage.SessionModeChat})
			if err != nil {
				t.Fatal(err)
			}
			id = row.ID
			if err := a.HandleMessage(ctx, "hello"); err != nil {
				t.Fatal(err)
			}
		}
		if executed != "" {
			t.Fatalf("chat executed tool: %q", executed)
		}
		requests := f.chatRequests()
		if len(requests) != 1 || len(requests[0].Tools) != 0 {
			t.Fatalf("chat sent tools or entered tool loop: requests=%+v", requests)
		}
		messages, err := a.execution.dialogue.Messages.Repository.ListBySession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.Role == storage.RoleTool {
				t.Fatal("chat persisted tool transcript")
			}
		}
	})
}
