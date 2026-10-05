package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type inputBlockingTool struct {
	agentWrapperTool
	prepare func()
}

func (t inputBlockingTool) DiscoveryContent(context.Context) (string, bool, error) {
	t.prepare()
	return "", false, nil
}

type inputBlockingSkill struct {
	agentDetailTool
	prepare func()
}

func (t inputBlockingSkill) LoadDetail(context.Context) (tool.DetailBlock, error) {
	t.prepare()
	return t.DetailBlock(), nil
}

func TestInputPreloadRechecksAdmissionAfterPreparation(t *testing.T) {
	for _, input := range []string{"@tool:worker", "@skill:doc", "@tool:worker @skill:doc"} {
		for _, change := range []string{"new", "resume", "mode", "compact", "cancel"} {
			t.Run(input+"/"+change, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				p := &fakePlatform{}
				f := &fakeLLM{}
				a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, newTestStore(t))
				started, release, unblock := modelBarrier(t)
				var once sync.Once
				block := func() { once.Do(func() { close(started); <-release }) }
				registry := tool.NewRegistry()
				for _, candidate := range []tool.Tool{
					inputBlockingTool{agentWrapperTool: agentWrapperTool{name: "alpha"}, prepare: block},
					agentWrapperTool{name: "wrapper", hidden: true},
					inputBlockingSkill{agentDetailTool: agentDetailTool{name: "doc", source: tool.SourceSkillAgent, detail: "DETAIL", format: "elyph", ruleCard: "RULE", activate: []string{"wrapper"}}, prepare: block},
				} {
					if err := registry.Register(candidate); err != nil {
						t.Fatal(err)
					}
				}
				a.SetToolRuntime(registry, nil)
				a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"worker": {Tools: []string{"alpha"}}}})
				row, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Metadata: `{"unknown":9007199254740993}`})
				if err != nil {
					t.Fatal(err)
				}
				before := row.Metadata
				done := make(chan error, 1)
				go func() { done <- a.HandleMessage(ctx, input) }()
				awaitModelBarrier(t, started)
				// The preparation barrier must not retain Session admission.
				changeCtx, changeCancel := context.WithTimeout(context.Background(), time.Second)
				defer changeCancel()
				switch change {
				case "new", "resume":
					if err := a.HandleMessage(changeCtx, "/new"); err != nil {
						t.Fatal(err)
					}
					if change == "resume" {
						if err := a.HandleMessage(changeCtx, "/resume "+row.ID); err != nil {
							t.Fatal(err)
						}
					}
				case "mode":
					if _, err := a.sessions.ActivateMode(changeCtx, a.scope(changeCtx), session.ActivateModeRequest{Mode: storage.SessionModeChat}); err != nil {
						t.Fatal(err)
					}
				case "compact":
					_, leave, err := a.sessions.EnterActivation(changeCtx, a.scope(changeCtx), row.ID)
					if err != nil {
						t.Fatal(err)
					}
					started := a.turns.StartCompactRun(row.ID, "compact")
					leave()
					if !started {
						t.Fatal("compact admission failed")
					}
					defer a.turns.CompleteCompactRun(row.ID, "compact")
				case "cancel":
					cancel()
				}
				unblock()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("invalidated preparation was accepted")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("input did not finish")
				}
				latest, err := a.store.Sessions().Get(context.Background(), row.ID)
				if err != nil {
					t.Fatal(err)
				}
				if latest.Metadata != before {
					t.Fatalf("rejected preload changed metadata: %s", latest.Metadata)
				}
				if strings.Contains(p.out.String(), "已注入") || strings.Contains(p.out.String(), "已存在") {
					t.Fatalf("rejected preload reported success: %s", p.out.String())
				}
				if f.requestCount() != 0 {
					t.Fatal("rejected input reached model")
				}
			})
		}
	}
}

func TestCompactingInputSkipsInputHookAndPreparation(t *testing.T) {
	for _, input := range []string{"hello", "@tool:alpha", "@skill:doc"} {
		t.Run(input, func(t *testing.T) {
			ctx := context.Background()
			a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
			hooks := hook.NewManager()
			if err := hooks.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "must-not-run", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
				t.Error("input hook ran during compaction")
				return e, nil
			})}); err != nil {
				t.Fatal(err)
			}
			a.setTestHookManager(hooks)
			registry := tool.NewRegistry()
			block := func() { t.Error("preparation ran during compaction") }
			if err := registry.Register(inputBlockingTool{agentWrapperTool: agentWrapperTool{name: "alpha"}, prepare: block}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(inputBlockingSkill{agentDetailTool: agentDetailTool{name: "doc", detail: "DETAIL", source: tool.SourceSkillAgent}, prepare: block}); err != nil {
				t.Fatal(err)
			}
			a.SetToolRuntime(registry, nil)
			row, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !a.turns.StartCompactRun(row.ID, "compact") {
				t.Fatal("compact not started")
			}
			defer a.turns.CompleteCompactRun(row.ID, "compact")
			if err := a.HandleMessage(ctx, input); err != nil {
				t.Fatal(err)
			}
		})
	}
}
