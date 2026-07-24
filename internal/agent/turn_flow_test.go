package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/turn"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTurnResponseTimeoutNotifiesUser(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	a := New(p, &fakeLLM{chatBlocks: []fakeLLMBlock{block}}, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.responseTimeout = 10 * time.Millisecond

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "本轮处理已超时停止") {
		t.Fatalf("platform output = %q, want timeout notice", got)
	}
}

func TestTurnResponseTimeoutZeroAllowsLongTurn(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	a := New(p, &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"done"}}, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.responseTimeout = 0

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(context.Background(), "hello") }()
	<-block.started
	time.Sleep(20 * time.Millisecond)
	close(block.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleMessage: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleMessage did not finish after release")
	}
	if got := p.out.String(); !strings.Contains(got, "done") {
		t.Fatalf("platform output = %q, want final reply", got)
	}
}

func TestStreamingOutputAppendsRawAndReplacesHookText(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{
		{DeltaContent: "hello "},
		{DeltaContent: "[[wave]]"},
	}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.replace", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = strings.ReplaceAll(event.LLM.Text, "[[wave]]", "world")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "hello [[wave]]" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "hello world" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
	if p.stream.finished != 1 {
		t.Fatalf("stream finished = %d", p.stream.finished)
	}
	if strings.Contains(p.out.String(), "hello world") {
		t.Fatalf("streaming output should not also send normal chat: %q", p.out.String())
	}
}

func TestTurnOutputPreparedHookReplacesFinalStreamingMessage(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "猫"}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentTurnOutputPrepared, Name: "test.turn_output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile("猫"), "狗", true)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register turn output hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "猫" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "狗" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
}

func TestLLMInterruptKeepsAppendConfirmationUntilConfirm(t *testing.T) {
	p := &fakePlatform{}
	block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"interrupted", "confirmed"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "1+1") }()
	select {
	case <-block.started:
	case <-time.After(time.Second):
		t.Fatal("first LLM request did not start")
	}
	waitRequestCount(t, f, 1)

	if err := a.HandleMessage(ctx, "stop"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interrupted turn: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted turn did not finish")
	}

	if err := a.HandleMessage(ctx, "同时计算2+2"); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.requestCount(); got != 1 {
		t.Fatalf("request count after pending append = %d, want 1", got)
	}

	if err := a.HandleMessage(ctx, "再计算3+3"); err != nil {
		t.Fatalf("append pending 2: %v", err)
	}
	if err := a.HandleMessage(ctx, "y"); err != nil {
		t.Fatalf("confirm append: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(requests))
	}
	got := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	for _, want := range []string{"1+1", "stop", "同时计算2+2", "再计算3+3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirmed request content = %q, missing %q", got, want)
		}
	}
}

func TestActiveTurnBlocksNewSessionCommand(t *testing.T) {
	tests := []struct {
		name  string
		phase turn.Phase
		start func(*testing.T, *Agent, string)
	}{
		{name: "llm", phase: turn.PhaseLLM, start: func(t *testing.T, a *Agent, sessionID string) {
			if !a.turns.StartLLM(sessionID, "input") {
				t.Fatal("StartLLM returned false")
			}
		}},
		{name: "tool", phase: turn.PhaseTool, start: func(t *testing.T, a *Agent, sessionID string) {
			if !a.turns.StartLLM(sessionID, "input") || !a.turns.StartToolPhase(sessionID) {
				t.Fatal("failed to enter tool phase")
			}
		}},
		{name: "await_append_confirm", phase: turn.PhaseAwaitAppendConfirm, start: func(t *testing.T, a *Agent, sessionID string) {
			if !a.turns.StartLLM(sessionID, "input") || !a.turns.InterruptLLM(sessionID, "more") {
				t.Fatal("failed to enter append confirmation phase")
			}
		}},
		{name: "await_risk_confirm", phase: turn.PhaseAwaitRiskConfirm, start: func(t *testing.T, a *Agent, sessionID string) {
			if !a.turns.StartLLM(sessionID, "input") || !a.turns.StartToolPhase(sessionID) {
				t.Fatal("failed to enter tool phase")
			}
			done := make(chan struct{})
			go func() {
				_, _ = a.turns.AwaitRiskConfirmation(sessionID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell"})
				close(done)
			}()
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && a.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitRiskConfirm {
				time.Sleep(10 * time.Millisecond)
			}
			if a.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitRiskConfirm {
				t.Fatal("did not enter risk confirmation phase")
			}
			t.Cleanup(func() {
				a.turns.StopSession(sessionID)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("risk confirmation did not stop")
				}
			})
		}},
		{name: "compact", phase: turn.PhaseCompact, start: func(t *testing.T, a *Agent, sessionID string) {
			if !a.turns.StartCompact(sessionID) {
				t.Fatal("StartCompact returned false")
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := &fakePlatform{}
			a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
			ctx := context.Background()
			current, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "current"})
			if err != nil {
				t.Fatal(err)
			}
			test.start(t, a, current.ID)

			if err := a.HandleMessage(ctx, "/new"); err != nil {
				t.Fatalf("/new: %v", err)
			}
			after, err := a.sessions.Current(ctx, a.scope(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if after.ID != current.ID {
				t.Fatalf("current session = %s, want %s", after.ID, current.ID)
			}
			if got := a.turns.Snapshot(current.ID).Phase; got != test.phase {
				t.Fatalf("turn phase = %s, want %s", got, test.phase)
			}
			if got := p.out.String(); got != activeTurnCommandBlockedText() {
				t.Fatalf("output = %q, want %q", got, activeTurnCommandBlockedText())
			}
		})
	}
}

func TestActiveTurnBlocksAllSessionSwitchCommands(t *testing.T) {
	for _, text := range []string{"/new", "/resume", "/fork missing", "/work", "/chat"} {
		t.Run(strings.TrimPrefix(text, "/"), func(t *testing.T) {
			p := &fakePlatform{}
			a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
			ctx := context.Background()
			current, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "current"})
			if err != nil {
				t.Fatal(err)
			}
			if !a.turns.StartLLM(current.ID, "input") {
				t.Fatal("StartLLM returned false")
			}

			if err := a.HandleMessage(ctx, text); err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			after, err := a.sessions.Current(ctx, a.scope(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if after.ID != current.ID {
				t.Fatalf("current session = %s, want %s", after.ID, current.ID)
			}
			if got := p.out.String(); got != activeTurnCommandBlockedText() {
				t.Fatalf("output = %q, want %q", got, activeTurnCommandBlockedText())
			}
		})
	}
}

func TestStopAllowsSessionSwitchAfterActiveTurn(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSessionIdleExpiration(config.SessionIdleExpirationConfig{GroupUserTTLMinutes: 10})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "1", ScopeID: "group:9"})
	current, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "current"})
	if err != nil {
		t.Fatal(err)
	}
	current.UpdatedAt = time.Now().Add(-11 * time.Minute)
	if err := a.store.Sessions().Update(ctx, current); err != nil {
		t.Fatalf("age current session: %v", err)
	}
	if !a.turns.StartLLM(current.ID, "input") {
		t.Fatal("StartLLM returned false")
	}
	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatalf("/stop: %v", err)
	}
	if got := a.turns.Snapshot(current.ID).Phase; got != turn.PhaseIdle {
		t.Fatalf("turn phase = %s, want idle", got)
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("/new: %v", err)
	}
	if _, err := a.sessions.Current(ctx, a.scope(ctx)); err == nil {
		t.Fatal("current session still exists after /new")
	}
}

func TestStopFinishesRuntimeStatus(t *testing.T) {
	p := &fakePlatform{}
	started := make(chan struct{})
	release := make(chan struct{})
	f := &fakeLLM{
		replies:    []string{"should not finish"},
		chatBlocks: []fakeLLMBlock{{started: started, release: release}},
	}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "等待停止") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("LLM did not start")
	}
	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("chat returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not stop")
	}

	_, status := p.statusSnapshot()
	if status.Phase != runtimestatus.PhaseDone {
		t.Fatalf("runtime phase = %s, want %s", status.Phase, runtimestatus.PhaseDone)
	}
	if status.FinishedAt.IsZero() {
		t.Fatal("runtime status has no finished time")
	}
	wantElapsed := status.FinishedAt.Sub(status.TurnStartedAt)
	if got := status.Elapsed(status.FinishedAt.Add(time.Minute)); got != wantElapsed {
		t.Fatalf("elapsed kept running after stop: got %s want %s", got, wantElapsed)
	}
}

func TestRiskConfirmationDetailShowsFullArgumentsWithoutResolving(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm detail"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	fullArgs := `{"cmd":"echo 12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890 > out.txt"}`
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: fullArgs, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	detailOutput := p.out.String()
	if strings.Contains(detailOutput, fullArgs) {
		t.Fatalf("detail should format args instead of raw JSON: %q", detailOutput)
	}
	for _, want := range []string{"cmd: ", "echo 1234567890", " > out.txt"} {
		if !strings.Contains(detailOutput, want) {
			t.Fatalf("detail missing %q: %q", want, detailOutput)
		}
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
	if got := a.turns.Snapshot(session.ID).Phase; got != turn.PhaseAwaitRiskConfirm {
		t.Fatalf("phase = %s, want await risk confirm", got)
	}
	if err := a.HandleMessage(ctx, "/confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	select {
	case resp := <-done:
		if !resp.Confirmed {
			t.Fatalf("response = %#v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("confirm did not resolve")
	}
}

func TestRiskConfirmationDetailFormatsEscapedNewlines(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm detail newlines"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "edit_file", Arguments: `{"path":"a.txt","content":"line 1\nline 2\nline 3"}`, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	out := p.out.String()
	if strings.Contains(out, `line 1\nline 2`) {
		t.Fatalf("detail still contains escaped newlines: %q", out)
	}
	for _, want := range []string{"content: |\n", "  line 1\n", "  line 2\n", "  line 3\n", "path: a.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q: %q", want, out)
		}
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
}

func TestRiskConfirmationCompletionAndConfirmAlias(t *testing.T) {

	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm completion"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: `{\"cmd\":\"rm x\"}`, Risk: "high"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if !containsAll(a.Complete("/c"), []string{"/confirm", "/c", "/confirmtool", "/ct", "/confirmall", "/ca"}) {
		t.Fatalf("confirm completion /c = %#v", a.Complete("/c"))
	}
	for _, command := range []string{"detail", "details", "confirm", "c", "confirmtool", "ct", "confirmall", "ca", "reject", "stop"} {

		if got := a.Complete("/" + command); len(got) == 0 {
			t.Fatalf("missing completion for %s", command)
		}
	}
	if err := a.HandleMessage(ctx, "/c"); err != nil {
		t.Fatalf("confirm alias: %v", err)
	}
	select {
	case resp := <-done:
		if !resp.Confirmed {
			t.Fatalf("response = %#v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmation alias did not resolve")
	}
}
