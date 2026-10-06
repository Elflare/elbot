package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// Build the execution graph directly: none of these participants need Agent.
type executionFixture struct {
	opts          Options
	execution     *executionCoordinator
	chat          *chatRunner
	confirmations *confirmationCoordinator
	hooks         *hook.DefaultManager
	out           turnOutput
}

func newExecutionFixture(t *testing.T, client llm.LLM, store storage.Store) *executionFixture {
	t.Helper()
	models := newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.LLM{"default": client}, Providers: map[string]config.ProviderConfig{"default": {}},
		ModeModels: map[string]config.ModelSelection{
			storage.SessionModeWork: {Provider: "default", Model: "model"},
			storage.SessionModeChat: {Provider: "default", Model: "model"},
		}, DefaultMode: storage.SessionModeWork,
	})
	opts := assembleTestOptions(testAgentOptions{Models: models, Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{DefaultMode: storage.SessionModeWork}})
	identity := &identityResolver{platformName: "cli", actorID: "cli:local", scopeID: "local", policy: opts.SecurityPolicy}
	hooks := hook.NewManager()
	bridge := &hookBridge{manager: hooks, requests: opts.Requests, identity: identity, dispatcher: opts.Dispatcher}
	status := &statusRecorder{}
	output := &outputSender{dispatcher: opts.Dispatcher, notifications: opts.Notifications, hooks: bridge, identity: identity}
	view := executionView{sessions: store.Sessions()}
	policy := &confirmationPolicy{identity: identity, userConfirmationTimeout: time.Second}
	confirmations := &confirmationCoordinator{
		sessions: opts.Sessions, requests: opts.Requests, turns: opts.Turns, commands: opts.Commands,
		identity: identity, output: output, policy: policy,
		autoConfirmSession: map[string]bool{}, autoConfirmTools: map[string]map[string]bool{},
	}
	runtime := toolRuntimeState{provider: noopToolSchemaProvider{}, defaultProvider: true, config: opts.ToolsConfig, manager: opts.ToolRunner}
	deps := &toolRunDeps{hooks: bridge, requests: opts.Requests, turns: opts.Turns, identity: identity,
		state: opts.ToolState, runtime: &runtime, sessions: opts.Sessions, store: store, confirmations: confirmations, view: view}
	caller := &modelCaller{messages: store.Messages(), hooks: bridge, identity: identity, toolState: opts.ToolState, toolRuntime: &runtime}
	chat := &chatRunner{
		messages: store.Messages(), contexts: opts.Contexts, models: models, turns: opts.Turns, identity: identity,
		hooks: bridge, view: view, toolRuntime: &runtime, toolState: opts.ToolState, toolDeps: deps, caller: caller,
		replies:       &replyCommitter{messages: store.Messages(), output: output},
		promptBuilder: PromptBuilder{System: NewSystemPromptManager(soulSystemPromptSource{Soul: staticSoulProvider{Prompt: "test"}})},
	}
	execution := &executionCoordinator{sessions: opts.Sessions, sessionRows: store.Sessions(), turns: opts.Turns,
		requests: opts.Requests, contexts: opts.Contexts, models: models, chat: chat, identity: identity,
		view: view, output: output, status: status, waitPolicy: policy, appendWaits: newAppendWaitLifecycle(t.Context())}
	opts.Sessions.SetForegroundActivation(execution.AdoptForeground)
	t.Cleanup(opts.Turns.StopAll)
	t.Cleanup(func() { _ = opts.Sessions.Close(context.Background()) })
	return &executionFixture{opts: opts, execution: execution, chat: chat, confirmations: confirmations, hooks: hooks, out: foregroundTurnOutput{sender: output, status: status}}
}

func (f *executionFixture) begin(t *testing.T) (context.Context, context.Context, chatTurnInput, *turn.Execution) {
	t.Helper()
	ctx, row, err := f.execution.resolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	execution := turn.NewExecution(storage.NewID())
	ctx = turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
	if !f.opts.Turns.StartExecution(row.ID, turn.Input{Text: "hello"}, execution, "attempt") {
		t.Fatal("start attempt")
	}
	prepared, err := f.chat.prepareTurn(ctx, row, "hello")
	if err != nil {
		t.Fatal(err)
	}
	info, requestCtx, done, err := f.opts.Requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(done)
	return ctx, withTurnRequestID(requestCtx, info.ID), chatTurnInput{Session: row, Text: "hello", Selection: modelSelectionForTurn(ctx, f.opts.Models, row), RequestID: info.ID, Prepared: prepared}, execution
}

func (f *executionFixture) on(t *testing.T, point hook.Point, fn func(hook.Event) hook.Event) {
	t.Helper()
	if err := f.hooks.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
		return fn(e), nil
	})}); err != nil {
		t.Fatal(err)
	}
}

func TestChatRunnerStructuredOutcomes(t *testing.T) {
	for _, name := range []string{"completed", "failed", "canceled", "paused", "superseded", "stopped"} {
		t.Run(name, func(t *testing.T) {
			model := &fakeLLM{replies: []string{"done"}}
			want := chatTurnCompleted
			if name == "failed" {
				model.replies = []string{"__ERR__"}
				want = chatTurnFailed
			}
			f := newExecutionFixture(t, model, newTestStore(t))
			ctx, requestCtx, in, execution := f.begin(t)
			if name != "failed" {
				f.on(t, hook.PointLLMResponseReceived, func(e hook.Event) hook.Event {
					switch name {
					case "completed":
						f.opts.Turns.StartToolPhase(in.Session.ID, "attempt")
						f.opts.Turns.AppendPendingInput(in.Session.ID, turn.Input{Text: "next"})
					case "canceled":
						want = chatTurnCanceled
						f.opts.Requests.CancelSession(in.Session.ID)
					case "paused", "superseded":
						want = chatTurnPaused
						f.opts.Turns.InterruptLLMInput(in.Session.ID, turn.Input{Text: "append"})
						f.opts.Requests.CancelSession(in.Session.ID)
						if name == "superseded" {
							want = chatTurnSuperseded
							merged, same, ok := f.opts.Turns.ResumeAppend(in.Session.ID)
							if !ok || same != execution || !f.opts.Turns.StartExecution(in.Session.ID, merged, same, "next-attempt") {
								t.Fatal("resume attempt")
							}
						}
					case "stopped":
						want = chatTurnStopped
						f.opts.Turns.StopSession(in.Session.ID, "attempt")
						e.LLM.ToolCalls = []llm.ToolCallRequest{{ID: "stopped-tool", Name: "unused"}}
					}
					return e
				})
			}
			result := f.chat.RunTurn(ctx, requestCtx, in, f.out)
			if result.Outcome != want {
				t.Fatalf("outcome=%v want=%v err=%v", result.Outcome, want, result.Err)
			}
			if name != "stopped" {
				select {
				case <-execution.Done():
					t.Fatal("single turn finished logical execution")
				default:
				}
			}
			if name == "completed" {
				if !result.Committed.Persisted || result.Committed.RawText != "done" {
					t.Fatalf("commit=%+v", result.Committed)
				}
				if pending := f.opts.Turns.DrainMergedInput(in.Session.ID, "attempt"); pending.Text != "next" {
					t.Fatalf("runner consumed next-turn pending: %+v", pending)
				}
				if _, active := f.opts.Requests.Get(in.RequestID); !active {
					t.Fatal("runner cleaned up coordinator-owned request")
				}
			}
			if name == "superseded" && !f.opts.Turns.MatchesAttempt(in.Session.ID, "next-attempt") {
				t.Fatal("old runner disturbed resumed attempt")
			}
		})
	}
}

func TestExecutionCommitDuringAppendKeepsUsage(t *testing.T) {
	f := newExecutionFixture(t, &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "committed", Usage: &llm.Usage{TotalTokens: 7}}}}}, newTestStore(t))
	ctx, row, err := f.execution.resolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	execution := turn.NewExecution(storage.NewID())
	ctx = turn.WithExecution(ctx, execution)
	f.on(t, hook.PointAgentTurnOutputPrepared, func(e hook.Event) hook.Event {
		if !f.opts.Turns.InterruptLLMInput(row.ID, turn.Input{Text: "append during commit"}) {
			t.Fatal("interrupt commit")
		}
		f.opts.Requests.CancelSession(row.ID)
		return e
	})
	if err := f.execution.Run(ctx, row, "hello", f.out); err != nil {
		t.Fatal(err)
	}
	latest, err := f.opts.Store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usage := f.execution.usageForSession(latest); usage == nil || usage.TotalTokens != 7 {
		t.Fatalf("successful commit lost usage: %+v", usage)
	}
	if f.opts.Turns.Snapshot(row.ID).Phase != turn.PhaseAwaitAppendConfirm {
		t.Fatal("commit consumed append confirmation")
	}
	select {
	case <-execution.Done():
		t.Fatal("commit ended paused execution")
	default:
	}
	if len(f.opts.Requests.ListBySession(row.ID)) != 0 {
		t.Fatal("attempt request remains active")
	}
}

type executionTimingStore struct {
	storage.Store
	messages storage.MessageRepository
}

func (s executionTimingStore) Messages() storage.MessageRepository { return s.messages }

type executionTimingMessages struct {
	storage.MessageRepository
	load   func() error
	append func(storage.Message) error
}

func (r executionTimingMessages) ListBySession(ctx context.Context, id string) ([]storage.Message, error) {
	if err := r.load(); err != nil {
		return nil, err
	}
	return r.MessageRepository.ListBySession(ctx, id)
}

func (r executionTimingMessages) Append(ctx context.Context, message *storage.Message) error {
	if err := r.append(*message); err != nil {
		return err
	}
	return r.MessageRepository.Append(ctx, message)
}

func TestExecutionRequestBoundaryAndCleanup(t *testing.T) {
	for _, failureAt := range []string{"", "load", "user"} {
		t.Run("failure_"+failureAt, func(t *testing.T) {
			store := newTestStore(t)
			var f *executionFixture
			var events []string
			failure := errors.New("persistence failure")
			repo := executionTimingMessages{MessageRepository: store.Messages(), load: func() error {
				events = append(events, "load")
				if len(f.opts.Requests.List()) != 0 {
					t.Fatal("request registered before material loading")
				}
				if failureAt == "load" {
					return failure
				}
				return nil
			}, append: func(message storage.Message) error {
				events = append(events, string(message.Role))
				active := f.opts.Requests.ListBySession(message.SessionID)
				if len(active) != 1 || active[0].Kind != request.KindTurn {
					t.Fatalf("persistence outside turn request: %+v", active)
				}
				if failureAt == "user" && message.Role == storage.RoleUser {
					return failure
				}
				return nil
			}}
			f = newExecutionFixture(t, &fakeLLM{replies: []string{"done"}}, executionTimingStore{Store: store, messages: repo})
			f.on(t, hook.PointLLMTurnPrepared, func(e hook.Event) hook.Event {
				events = append(events, "prepared")
				if len(f.opts.Requests.ListBySession(e.Session.ID)) != 1 {
					t.Fatal("prepared hook without turn request")
				}
				return e
			})
			ctx, row, err := f.execution.resolveInput(context.Background(), "hello")
			if err != nil {
				t.Fatal(err)
			}
			execution := turn.NewExecution(storage.NewID())
			ctx = turn.WithExecution(ctx, execution)
			err = f.execution.Run(ctx, row, "hello", f.out)
			if (failureAt != "") != errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if len(f.opts.Requests.List()) != 0 || f.opts.Turns.Snapshot(row.ID).Phase != turn.PhaseIdle {
				t.Fatal("request or turn leaked")
			}
			select {
			case <-execution.Done():
			default:
				t.Fatal("logical execution did not finish")
			}
			if failureAt == "" && !reflect.DeepEqual(events, []string{"load", "prepared", "user", "assistant"}) {
				t.Fatalf("lifecycle order=%v", events)
			}
		})
	}
}

func TestConfirmationCoordinatorPreservesAutoConfirmationScope(t *testing.T) {
	for _, command := range []string{"/confirmtool extra", "/confirmall extra", "/reject reason", "/stop"} {
		t.Run(command, func(t *testing.T) {
			f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
			ctx, requestCtx, in, _ := f.begin(t)
			if !f.opts.Turns.StartToolPhase(in.Session.ID, "attempt") {
				t.Fatal("start tools")
			}
			done := make(chan toolrun.ConfirmResult, 1)
			go func() {
				result, _ := f.confirmations.AwaitToolConfirmation(requestCtx, in.Session.ID, llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, tool.RiskAssessment{Level: tool.RiskHigh}, "detail")
				done <- result
			}()
			deadline := time.Now().Add(time.Second)
			for f.opts.Turns.Snapshot(in.Session.ID).Phase != turn.PhaseAwaitRiskConfirm && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if f.opts.Turns.Snapshot(in.Session.ID).Phase != turn.PhaseAwaitRiskConfirm {
				t.Fatal("confirmation not pending")
			}
			if err := f.confirmations.SubmitResponse(ctx, in.Session.ID, command); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				allowed := command == "/confirmtool extra" || command == "/confirmall extra"
				if result.Allowed != allowed || result.Stopped != (command == "/stop") || (allowed && result.Extra != "extra") {
					t.Fatalf("confirmation result=%+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("confirmation did not finish")
			}
			if f.confirmations.isSessionAutoConfirmed(in.Session.ID) != (command == "/confirmall extra") || f.confirmations.isToolAutoConfirmed(in.Session.ID, "danger") != (command == "/confirmtool extra") {
				t.Fatal("wrong automatic confirmation scope")
			}
			if f.confirmations.isSessionAutoConfirmed("another-session") || f.confirmations.isToolAutoConfirmed("another-session", "danger") || f.confirmations.isToolAutoConfirmed(in.Session.ID, "another-tool") {
				t.Fatal("automatic confirmation escaped its scope")
			}
		})
	}
}
