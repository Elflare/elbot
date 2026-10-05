package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

type statusReadingPlatform struct {
	fakePlatform
	recorder *statusRecorder
	observed chan runtimestatus.Snapshot
}

func (p *statusReadingPlatform) SetRuntimeStatus(_ context.Context, sent runtimestatus.Snapshot) error {
	p.observed <- p.recorder.Snapshot(sent.SessionID)
	return errors.New("display unavailable")
}

func TestComponentStatusRecordedBeforeDisplayAndBackgroundStaysSilent(t *testing.T) {
	recorder := &statusRecorder{}
	p := &statusReadingPlatform{recorder: recorder, observed: make(chan runtimestatus.Snapshot, 2)}
	foreground := foregroundTurnOutput{sender: &outputSender{dispatcher: dispatch.New(dispatch.Options{Primary: p})}, status: recorder}
	background := backgroundTurnOutput{status: recorder}
	started := time.Now()
	background.PublishRuntimeStatus(context.Background(), runtimestatus.Snapshot{SessionID: "s1", Model: "model", TurnStartedAt: started, Phase: runtimestatus.PhaseLLM})
	if len(p.observed) != 0 || recorder.Snapshot("s1").Model != "model" {
		t.Fatal("background must record without displaying")
	}
	done := make(chan struct{})
	go func() {
		foreground.PublishRuntimeStatus(context.Background(), runtimestatus.Snapshot{SessionID: "s1", Phase: runtimestatus.PhaseDone})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("display callback blocked reading the recorded snapshot")
	}
	got := <-p.observed
	if got.Phase != runtimestatus.PhaseDone || got.Model != "model" || !got.TurnStartedAt.Equal(started) {
		t.Fatalf("display read stale or unmerged state: %+v", got)
	}
	if recorder.Snapshot("s1").Phase != runtimestatus.PhaseDone {
		t.Fatal("display failure changed local state")
	}
	// Concurrent sessions use the same recorder without sharing a state lock
	// with display delivery. Race detection covers both reads and merges.
	var wg sync.WaitGroup
	for _, id := range []string{"s1", "s2", "s3"} {
		wg.Go(func() {
			for range 30 {
				recorder.Record(runtimestatus.Snapshot{SessionID: id, Phase: runtimestatus.PhaseLLM})
				_ = recorder.Snapshot(id)
			}
		})
	}
	wg.Wait()
}

type componentAdminTool struct{ agentDetailTool }

func (t componentAdminTool) Info() tool.Info {
	info := t.agentDetailTool.Info()
	info.SuperadminOnly = true
	return info
}

func TestComponentPolicyUpdateReachesHookAndPrompt(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	registry := tool.NewRegistry()
	if err := registry.Register(componentAdminTool{agentDetailTool{name: "admin_only"}}); err != nil {
		t.Fatal(err)
	}
	a.SetToolRuntime(registry, nil)
	provider := a.toolRuntime.provider.(toolRunPromptProvider)
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{Source: chatinfo.Source{Platform: "qq", ScopeID: "private:1"}, Identity: chatinfo.Identity{PlatformUserID: "1"}})
	for _, admin := range []bool{false, true, false} {
		admins := map[string][]string{}
		if admin {
			admins["qq"] = []string{"1"}
		}
		a.SetSecurityPolicy(security.NewPolicy("low", "high", admins))
		actor := a.identity.Actor(ctx)
		event := a.hooks.fillContext(ctx, hook.Event{})
		if (actor.Role == security.RoleSuperadmin) != admin || event.Actor.Role != string(actor.Role) {
			t.Fatalf("identity and Hook diverged after policy update: %+v / %+v", actor, event.Actor)
		}
		names, err := provider.ToolNames(ctx, storage.SessionModeWork, &storage.Session{ID: "s1"}, a.Scope(ctx))
		if err != nil || slices.Contains(names.Tools, "admin_only") != admin {
			t.Fatalf("Prompt kept stale identity: %+v / %v", names, err)
		}
		if sourceFree := a.hooks.fillContext(context.Background(), hook.Event{}); sourceFree.Actor.ID != "" {
			t.Fatal("policy update introduced a default Hook identity")
		}
	}
}

type componentLogs struct{ logger *slog.Logger }

func (l componentLogs) Runtime() *slog.Logger { return l.logger }
func (l componentLogs) Audit() *slog.Logger   { return l.logger }

func TestComponentLoggerReplacementReachesHookOutputAndReply(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "failing", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
		return event, errors.New("hook failure")
	})}); err != nil {
		t.Fatal(err)
	}
	a.setTestHookManager(manager)
	var replyEvents []string
	a.replies.messages = &replyTestRepository{MessageRepository: a.store.Messages(), events: &replyEvents, mapErr: errors.New("association failure")}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Sender: mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{}, errors.New("send failure")
	})})
	emit := func() {
		a.hooks.Notify(ctx, hook.Event{Point: hook.PointErrorOccurred})
		if _, err := a.output.SendAssistant(ctx, "hello"); err == nil {
			t.Fatal("expected send failure")
		}
		a.replies.associateReceipt(ctx, "session", "message", delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "qq", ScopeID: "private:1", PlatformMessageID: "sent"}}})
	}
	var before, after bytes.Buffer
	a.SetLogger(slog.New(slog.NewTextHandler(&before, nil)))
	emit()
	if !strings.Contains(before.String(), "hook error") || !strings.Contains(before.String(), "chat send failed") || !strings.Contains(before.String(), "map platform message failed") {
		t.Fatalf("SetLogger did not reach all components: %s", before.String())
	}
	before.Reset()
	a.SetLogManager(componentLogs{slog.New(slog.NewTextHandler(&after, nil))})
	emit()
	if before.Len() != 0 || strings.Count(after.String(), "hook error") != 1 || strings.Count(after.String(), "chat send failed") != 1 || strings.Count(after.String(), "map platform message failed") != 1 || strings.Count(after.String(), "map_platform_message") != 1 {
		t.Fatalf("logger replacement: old=%s new=%s", before.String(), after.String())
	}
	after.Reset()
	a.SetLogManager(nil)
	emit()
	if after.Len() != 0 {
		t.Fatal("component retained cleared logger")
	}
}

func TestExecutionViewKeepsRequestCancellationAndClearsBackgroundOverrides(t *testing.T) {
	execution := turn.NewExecution("run")
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	ctx := turn.WithAttempt(turn.WithExecution(requestCtx, execution), "attempt")
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "qq", ScopeID: "cron:1"}}, Sender: &fakePlatform{}, BufferAssistantOutput: true})
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Root: "/background", Dir: "/background/task"})
	ctx = context.WithValue(ctx, backgroundModelSelectionKey{}, config.ModelSelection{Provider: "task", Model: "task"})
	foreground, cancelForeground := context.WithCancel(chatinfo.WithInfo(context.Background(), chatinfo.Info{Source: chatinfo.Source{Platform: "cli", ScopeID: "remote:original"}}))
	actor := security.Actor{ID: "cli:owner", Role: security.RoleSuperadmin}
	execution.Adopt(security.WithActor(foreground, actor))
	cancelForeground()
	view := executionView{}
	refreshed := view.Context(ctx)
	msg, ok := platform.MessageContextFrom(refreshed)
	if !ok || msg.Info.Source.ScopeID != "remote:original" || msg.Sender != nil || msg.BufferAssistantOutput {
		t.Fatalf("retained background routing: %+v", msg)
	}
	if sandbox, _ := sandboxctx.SandboxContextFromContext(refreshed); sandbox.Root != "" || sandbox.Dir != "" {
		t.Fatalf("retained sandbox: %+v", sandbox)
	}
	if selection, _ := refreshed.Value(backgroundModelSelectionKey{}).(config.ModelSelection); selection.Provider != "" || selection.Model != "" {
		t.Fatalf("retained model override: %+v", selection)
	}
	if got, _ := security.ActorFromContext(refreshed); got != actor || turn.AttemptFromContext(refreshed) != "attempt" || refreshed.Err() != nil {
		t.Fatalf("lost request identity or inherited foreground cancellation: %+v / %v", got, refreshed.Err())
	}
	cancelRequest()
	if !errors.Is(refreshed.Err(), context.Canceled) || !errors.Is(view.Context(ctx).Err(), context.Canceled) {
		t.Fatal("execution view detached request cancellation")
	}
}

func TestComponentLoggerReplacementReachesExecutionChatModelToolsAndConfirmation(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{replies: []string{"one", "two", "three"}}, "model", config.ProviderConfig{}, newTestStore(t))
	ctx, row, err := a.execution.resolveInput(context.Background(), "logging")
	if err != nil {
		t.Fatal(err)
	}
	out := foregroundTurnOutput{sender: a.output, status: a.status}
	emit := func() {
		if _, err := a.chat.prepareTurn(ctx, row, "input"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.caller.Call(ctx, row, modelSelectionForTurn(ctx, a.models, row), nil, nil, nil, nil, out); err != nil {
			t.Fatal(err)
		}
		call := llm.ToolCallRequest{ID: "call", Name: "test_tool", Arguments: "{}"}
		a.toolDeps.RecordToolCall(ctx, row.ID, call, "low", storage.Now(), "done", nil)
		a.confirmations.logRiskConfirmationWait(row.ID, call, tool.RiskHigh, nil)
		a.execution.handleTurnContextDone(ctx, row.ID, context.DeadlineExceeded, out)
	}
	var before, after bytes.Buffer
	a.SetLogger(slog.New(slog.NewTextHandler(&before, nil)))
	emit()
	for _, message := range []string{"user input", "llm output", "turn response timeout", "msg=\"tool call\""} {
		if strings.Count(before.String(), message) != 1 {
			t.Fatalf("runtime message %q: %s", message, before.String())
		}
	}
	before.Reset()
	a.SetLogManager(componentLogs{slog.New(slog.NewTextHandler(&after, nil))})
	emit()
	if before.Len() != 0 {
		t.Fatal("component retained replaced logger")
	}
	for _, message := range []string{"user input", "llm output", "turn response timeout", "msg=\"tool call\"", "event=risk_confirmation_wait", "event=turn_response_timeout", "event=llm_usage"} {
		if strings.Count(after.String(), message) != 1 {
			t.Fatalf("replacement message %q: %s", message, after.String())
		}
	}
	after.Reset()
	a.SetLogManager(nil)
	emit()
	if after.Len() != 0 {
		t.Fatal("component retained cleared logger")
	}
}
