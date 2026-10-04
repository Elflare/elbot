package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/background"
	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type backgroundTestResult struct {
	result background.RunResult
	err    error
}

func takeoverPrivateContext() context.Context {
	return platform.WithMessageContext(context.Background(), platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "qq", ScopeID: "private:1", ConversationKind: chatinfo.ConversationPrivate}, Identity: chatinfo.Identity{ActorID: "qq:1", PlatformUserID: "1"}}})
}
func startTakeoverTest(a *Agent) <-chan backgroundTestResult {
	done := make(chan backgroundTestResult, 1)
	go func() {
		r, err := a.RunBackground(context.Background(), background.RunRequest{Kind: background.KindCron, Name: "takeover", Platform: "qq", Actor: security.Actor{ID: "qq:1", Platform: "qq", PlatformUserID: "1", Role: security.RoleSuperadmin}, Prompt: "original background task"})
		done <- backgroundTestResult{r, err}
	}()
	return done
}
func awaitTakeover(t *testing.T, done <-chan backgroundTestResult) background.RunResult {
	t.Helper()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.result
	case <-time.After(3 * time.Second):
		t.Fatal("background waiter did not complete")
		return background.RunResult{}
	}
}

func TestBackgroundTakeoverDuringToolSharesPending(t *testing.T) {
	p := &fakePlatform{}
	started, release := make(chan struct{}), make(chan struct{})
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "slow-call", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "foreground final"}},
	}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	registry := tool.NewRegistry()
	_ = registry.Register(slowTool{started: started, release: release})
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)
	done := startTakeoverTest(a)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	ctx := takeoverPrivateContext()
	id := f.chatRequests()[0].SessionID
	if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(ctx, "include this pending detail"); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := awaitTakeover(t, done)
	if !result.TakenOver || result.Text != "foreground final" || result.Outcome != "completed" {
		t.Fatalf("result: %#v", result)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	found := false
	for _, m := range requests[1].Messages {
		if strings.Contains(llm.SegmentsTextOnly(m.Segments), "include this pending detail") {
			found = true
		}
	}
	if !found {
		t.Fatal("pending input missing")
	}
	row, err := a.store.Sessions().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if session.IsBackground(row) || !session.WasPromoted(row) || row.PlatformScopeID != "private:1" {
		t.Fatalf("not permanently promoted: %#v", row)
	}
	if !strings.Contains(p.out.String(), "foreground final") {
		t.Fatalf("output: %s", p.out.String())
	}
}

func TestBackgroundTakeoverWaitsForAppendConfirmation(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"discarded", "confirmed foreground result"}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	done := startTakeoverTest(a)
	select {
	case <-block.started:
	case <-time.After(3 * time.Second):
		t.Fatal("LLM did not start")
	}
	id := f.chatRequests()[0].SessionID
	ctx := takeoverPrivateContext()
	if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(ctx, "new detail"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		t.Fatalf("waiter completed during confirmation: %#v", r)
	default:
	}
	if err := a.HandleMessage(ctx, "yes"); err != nil {
		t.Fatal(err)
	}
	result := awaitTakeover(t, done)
	if !result.TakenOver || result.Text != "confirmed foreground result" {
		t.Fatalf("result: %#v", result)
	}
}

func TestBackgroundCompactHandoff(t *testing.T) {
	for _, promote := range []bool{false, true} {
		name := "background"
		if promote {
			name = "promoted"
		}
		t.Run(name, func(t *testing.T) {
			p := &fakePlatform{}
			block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
			f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"compressed history", "finished after compact"}}
			a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
			a.RegisterPlatformSender("qq", p)
			req := background.RunRequest{Kind: background.KindCron, Name: "compact", Platform: "qq", Actor: security.Actor{ID: "qq:1", Platform: "qq", PlatformUserID: "1", Role: security.RoleSuperadmin}, Prompt: "accepted input"}
			row, err := a.backgroundSession(context.Background(), req, session.Scope{ActorID: "qq:1", Platform: "qq", PlatformScopeID: "cron:compact"})
			if err != nil {
				t.Fatal(err)
			}
			req.SessionID = row.ID
			for _, m := range []*storage.Message{{SessionID: row.ID, Role: storage.RoleUser, Content: "history"}, {SessionID: row.ID, Role: storage.RoleAssistant, Content: "old answer"}} {
				if err := a.store.Messages().Append(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: .8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil, config.ModelSelection{})
			a.recordUsage(row.ID, &llm.Usage{TotalTokens: 80})
			done := make(chan backgroundTestResult, 1)
			go func() {
				result, err := a.RunBackground(context.Background(), req)
				done <- backgroundTestResult{result, err}
			}()
			select {
			case <-block.started:
			case <-time.After(3 * time.Second):
				t.Fatal("compact did not start")
			}
			ctx := takeoverPrivateContext()
			if promote {
				if err := a.HandleMessage(ctx, "/resume "+row.ID); err != nil {
					t.Fatal(err)
				}
				if err := a.HandleMessage(ctx, "rejected during compact"); err != nil {
					t.Fatal(err)
				}
			}
			close(block.release)
			result := awaitTakeover(t, done)
			if result.SessionID == row.ID || result.Text != "finished after compact" || result.TakenOver != promote {
				t.Fatalf("result: %#v", result)
			}
			if phase := a.turns.Snapshot(row.ID).Phase; phase != turn.PhaseIdle {
				t.Fatalf("old turn: %s", phase)
			}
			next, err := a.store.Sessions().Get(ctx, result.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if session.IsBackground(next) == promote || session.WasPromoted(next) != promote {
				t.Fatalf("identity: %#v", next)
			}
			dir, err := a.workspaceStore(next).GetWorkspaceDir(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if dir == "" {
				t.Fatal("workspace lost")
			}
			requests := f.chatRequests()
			if len(requests) != 2 {
				t.Fatalf("requests: %d", len(requests))
			}
			for _, m := range requests[1].Messages {
				if strings.Contains(llm.SegmentsTextOnly(m.Segments), "rejected during compact") {
					t.Fatal("compact accepted new input")
				}
			}
			if promote {
				reused, err := a.RunBackground(ctx, req)
				if err != nil || !reused.TakenOver || len(f.chatRequests()) != 2 {
					t.Fatalf("background reuse: %#v %v", reused, err)
				}
			}
		})
	}
}

func TestBackgroundTakeoverStopRecordsCancellation(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"never returned"}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	done := startTakeoverTest(a)
	select {
	case <-block.started:
	case <-time.After(3 * time.Second):
		t.Fatal("LLM did not start")
	}
	id := f.chatRequests()[0].SessionID
	ctx := takeoverPrivateContext()
	if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) || !r.result.TakenOver || r.result.Outcome != "cancelled" || r.result.MessageID != "" {
			t.Fatalf("result: %#v %v", r.result, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not stop")
	}
}

func TestLateInterruptedRequestCannotFinishResumedExecution(t *testing.T) {
	p := &fakePlatform{}
	old := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{}), ignoreCancellation: true}
	next := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{old, next}, replies: []string{"late discarded answer", "current answer"}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	done := startTakeoverTest(a)
	select {
	case <-old.started:
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not start")
	}
	id := f.chatRequests()[0].SessionID
	ctx := takeoverPrivateContext()
	if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMessage(ctx, "new detail"); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan error, 1)
	go func() { resumed <- a.HandleMessage(ctx, "yes") }()
	select {
	case <-next.started:
	case <-time.After(3 * time.Second):
		t.Fatal("resumed request did not start")
	}
	execution := a.turns.Execution(id)
	close(old.release)
	// Wait for the old registered request to finish while the replacement is blocked.
	deadline := time.Now().Add(3 * time.Second)
	for len(a.requests.ListBySession(id)) > 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(a.requests.ListBySession(id)) > 1 {
		t.Fatal("old request did not finish")
	}
	if execution == nil || a.turns.Execution(id) != execution {
		t.Fatal("late request removed resumed execution")
	}
	select {
	case r := <-done:
		t.Fatalf("late request completed waiter: %#v", r)
	default:
	}
	close(next.release)
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resumed request hung")
	}
	result := awaitTakeover(t, done)
	if result.Text != "current answer" || !result.TakenOver {
		t.Fatalf("result: %#v", result)
	}
}

type foregroundContextProbe struct{ observed chan context.Context }

func (t foregroundContextProbe) Name() string { return "foreground_probe" }
func (t foregroundContextProbe) Info() tool.Info {
	return tool.Info{Name: t.Name(), Source: tool.SourceBuiltin, Risk: tool.RiskLow, ForegroundOnly: true, OwnerScoped: true}
}
func (t foregroundContextProbe) Schema() llm.ToolSchema {
	return llm.ToolSchema{Function: llm.ToolFunctionSchema{Name: t.Name(), Parameters: map[string]any{"type": "object"}}}
}
func (t foregroundContextProbe) Call(ctx context.Context, _ tool.CallRequest) (*tool.Result, error) {
	t.observed <- ctx
	return &tool.Result{Content: "foreground context observed"}, nil
}

func TestTakeoverRefreshesNextToolInSameBatch(t *testing.T) {
	p := &fakePlatform{}
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan context.Context, 1)
	sandboxRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sandboxRoot, "cron"), 0700); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{Index: 0, ID: "slow-call", Name: "slow", Args: "{}"}, {Index: 1, ID: "probe-call", Name: "foreground_probe", Args: "{}"}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	a.sandboxRoot = sandboxRoot
	registry := tool.NewRegistry()
	_ = registry.Register(slowTool{started: started, release: release})
	_ = registry.Register(foregroundContextProbe{observed})
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)
	done := startTakeoverTest(a)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow tool did not start")
	}
	id := f.chatRequests()[0].SessionID
	if err := a.HandleMessage(takeoverPrivateContext(), "/resume "+id); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := awaitTakeover(t, done)
	if !result.TakenOver {
		t.Fatal("not adopted")
	}
	select {
	case ctx := <-observed:
		actor, _ := security.ActorFromContext(ctx)
		binding, ok := session.BindingFromContext(ctx)
		if sandboxctx.BackgroundContext(ctx) || actor.Role != security.RoleUser || !ok || !binding.Valid() || binding.SessionID() != id {
			t.Fatalf("tool context: actor=%#v binding=%#v background=%v", actor, binding, sandboxctx.BackgroundContext(ctx))
		}
		dir, err := workspace.CurrentWorkspaceDir(context.WithoutCancel(ctx))
		if err != nil || dir == "" {
			t.Fatalf("workspace: %q %v", dir, err)
		}
	default:
		t.Fatal("foreground-only tool was not executed after adoption")
	}
	requests := f.chatRequests()
	if len(requests) != 2 || !strings.Contains(llm.SegmentsTextOnly(requests[1].Messages[0].Segments), "conversation=private") || !strings.Contains(llm.SegmentsTextOnly(requests[1].Messages[0].Segments), "tools: foreground_probe") {
		t.Fatal("foreground prompt was not rebuilt")
	}
}

type takeoverFinalGate struct {
	*fakePlatform
	started chan struct{}
	release chan struct{}
}

func (p *takeoverFinalGate) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	for _, out := range outputs {
		if out.Text == "first final" {
			close(p.started)
			select {
			case <-p.release:
			case <-ctx.Done():
				return delivery.Receipt{}, ctx.Err()
			}
		}
	}
	return p.fakePlatform.SendChat(ctx, outputs)
}

func TestTakeoverPendingContinuesThroughAutomaticCompact(t *testing.T) {
	p := &takeoverFinalGate{fakePlatform: &fakePlatform{}, started: make(chan struct{}), release: make(chan struct{})}
	toolStarted, toolRelease := make(chan struct{}), make(chan struct{})
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "slow", Name: "slow", Args: "{}"}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "first final", Usage: &llm.Usage{TotalTokens: 80}}},
		{{DeltaContent: "summary"}},
		{{DeltaContent: "second final"}},
	}}
	a := New(p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.RegisterPlatformSender("qq", p)
	registry := tool.NewRegistry()
	_ = registry.Register(slowTool{started: toolStarted, release: toolRelease})
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)
	a.SetContextOptions(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: .8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil, config.ModelSelection{})
	done := startTakeoverTest(a)
	select {
	case <-toolStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	id := f.chatRequests()[0].SessionID
	ctx := takeoverPrivateContext()
	if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
		t.Fatal(err)
	}
	runID := a.turns.Execution(id).ID
	close(toolRelease)
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("final send did not start")
	}
	if err := a.HandleMessage(ctx, "late accepted input"); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	result := awaitTakeover(t, done)
	if !result.TakenOver || result.SessionID == id || result.RunID != runID || result.Text != "second final" {
		t.Fatalf("result: %#v", result)
	}
	requests := f.chatRequests()
	if len(requests) != 4 {
		t.Fatalf("requests: %d", len(requests))
	}
	found := 0
	for _, message := range requests[3].Messages {
		found += strings.Count(llm.SegmentsTextOnly(message.Segments), "late accepted input")
	}
	if found != 1 {
		t.Fatalf("pending input occurrences=%d", found)
	}
}
