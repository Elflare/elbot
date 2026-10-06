package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func startAppendWait(t *testing.T, a *Agent, ctx context.Context) (context.Context, *storage.Session, *turn.Execution) {
	t.Helper()
	ctx, row, err := a.execution.resolveInput(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	execution := turn.NewExecution("append-test")
	if !a.execution.turns.StartExecution(row.ID, turn.Input{Text: "first"}, execution, "attempt") {
		t.Fatal("start execution")
	}
	_, requestCtx, done, err := a.execution.requests.Start(ctx, request.StartRequest{SessionID: row.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(done)
	if _, err := a.execution.AcceptInput(ctx, row, "more"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(requestCtx.Err(), context.Canceled) {
		t.Fatal("original model request was not canceled")
	}
	return ctx, row, execution
}

func TestAppendWaitSurvivesRequestAndSourceCancellation(t *testing.T) {
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{replies: []string{"continued"}}, "model", config.ProviderConfig{}, newTestStore(t))
	source, cancelSource := context.WithCancel(context.Background())
	ctx, row, execution := startAppendWait(t, a, source)
	cancelSource()
	if a.execution.turns.Snapshot(row.ID).Phase != turn.PhaseAwaitAppendConfirm {
		t.Fatal("request cancellation removed append confirmation")
	}
	if err := a.execution.ResumeAppend(context.WithoutCancel(ctx), row, "yes"); err != nil {
		t.Fatal(err)
	}
	if result := execution.Wait(context.Background()); result.Err != nil || result.Text != "continued" {
		t.Fatalf("continuation result: %+v", result)
	}
	if strings.Contains(p.out.String(), "追加确认已过期") {
		t.Fatal("resolved confirmation reported expiry")
	}
}

func TestAppendWaitShutdownIsSilent(t *testing.T) {
	for _, how := range []string{"parent", "close"} {
		for _, timed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timed=%v", how, timed), func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				p := &fakePlatform{}
				a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
					opts.RuntimeContext = parent
					opts.SessionIdleExpiration = &config.SessionIdleExpirationConfig{}
				})
				source := context.Background()
				if timed {
					source = security.WithActor(source, security.Actor{ID: "cli:review", Role: security.RoleUser})
				}
				_, row, execution := startAppendWait(t, a, source)
				if how == "parent" {
					cancel()
				} else {
					ctx, done := context.WithTimeout(context.Background(), time.Second)
					defer done()
					if err := a.Close(ctx); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-a.Done():
				case <-time.After(time.Second):
					t.Fatal("append wait survived shutdown")
				}
				if a.execution.turns.Snapshot(row.ID).Phase != turn.PhaseIdle {
					t.Fatal("shutdown left pending input")
				}
				if result := execution.Wait(context.Background()); !errors.Is(result.Err, context.Canceled) {
					t.Fatalf("execution result: %+v", result)
				}
				if strings.Contains(p.out.String(), "追加确认已过期") {
					t.Fatal("shutdown sent an expiry notification")
				}
				if err := a.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAppendExpiryCloseWaitsForOutput(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	hooks := hook.NewManager()
	if err := hooks.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Name: "blocked expiry", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, e hook.Event) (hook.Event, error) {
		if strings.Contains(llm.SegmentsTextOnly(e.Message.Segments), "追加确认已过期") {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return e, ctx.Err()
		}
		return e, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = hooks })
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	a.execution.waitPolicy.userConfirmationTimeout = 10 * time.Millisecond
	startAppendWait(t, a, security.WithActor(context.Background(), security.Actor{ID: "cli:review", Role: security.RoleUser}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expiry output did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close returned before output exited: %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("output did not receive cancellation")
	}
	select {
	case <-a.Done():
		t.Fatal("Done closed with output still active")
	default:
	}
	unblock.Do(func() { close(release) })
	select {
	case <-a.Done():
	case <-time.After(time.Second):
		t.Fatal("output exit did not finish shutdown")
	}
}

func TestAppendWaitRegistrationRacesClose(t *testing.T) {
	lifecycle := newAppendWaitLifecycle(context.Background())
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 64 {
		workers.Go(func() {
			<-start
			ctx, finish, ok := lifecycle.begin(context.Background())
			if ok {
				<-ctx.Done()
				finish()
			}
		})
	}
	close(start)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Close(ctx); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if _, _, ok := lifecycle.begin(context.Background()); ok {
		t.Fatal("accepted a wait after shutdown")
	}
}
