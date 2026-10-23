package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	chatroute "elbot/internal/agent/chat"
	agentconfirmation "elbot/internal/agent/confirmation"
	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	"elbot/internal/agent/routes"
	agentshare "elbot/internal/agent/share"
	agenttools "elbot/internal/agent/tools"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

// Build the execution graph directly: none of these participants need agentFixture.
type executionFixture struct {
	opts          testAssembly
	execution     *ExecutionCoordinator
	chat          *chatroute.Loop
	runner        *dialogue.Runner
	confirmations *agentconfirmation.ConfirmationCoordinator
	hooks         *hook.DefaultManager
	out           dialogue.Output
}

func newExecutionFixture(t *testing.T, client llm.Client, store storage.Store) *executionFixture {
	t.Helper()
	models := newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"default": client}, Providers: map[string]config.ProviderConfig{"default": {}},
		ModeModels: map[string]config.ModelSelection{
			storage.SessionModeWork: {Provider: "default", Model: "model"},
			storage.SessionModeChat: {Provider: "default", Model: "model"},
		}, DefaultMode: storage.SessionModeWork,
	})
	opts := assembleTestOptions(t, testAgentOptions{Models: models, Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{DefaultMode: storage.SessionModeWork}})
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Policy: opts.SecurityPolicy})
	hooks := newTestHookManager()

	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Manager: hooks, Requests: opts.Requests, Identity: identity, Dispatcher: opts.Dispatcher})
	status := NewStatusRecorder(StatusRecorderDeps{Turns: opts.Turns, View: dialogue.ExecutionView{Sessions: store.Sessions()}, Changed: agentevents.NewSignals().StatusChanged})
	output := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: opts.Dispatcher, Hooks: bridge})
	view := dialogue.ExecutionView{Sessions: store.Sessions()}
	policy := agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{Identity: identity, UserConfirmationTimeout: time.Second})
	confirmations := agentconfirmation.NewConfirmationCoordinator(agentconfirmation.ConfirmationCoordinatorDeps{Sessions: opts.Sessions, Requests: opts.Requests, Turns: opts.Turns, Commands: opts.Commands, Identity: identity, Output: output, Policy: policy})
	runtime := agenttools.NewToolRuntimeState(agenttools.ToolRuntimeStateDeps{Manager: opts.ToolRunner, Identity: identity, FileRollback: opts.FileRollback})
	deps := agenttools.NewToolRunDeps(agenttools.ToolRunDeps{Status: status, Hooks: bridge, Requests: opts.Requests, Turns: opts.Turns, Identity: identity, State: opts.ToolState, Runtime: runtime, Sessions: opts.Sessions, SessionRows: store.Sessions(), ToolCalls: store.ToolCalls(), Confirmations: confirmations, View: view})
	tools := &dialogue.ToolExecutor{
		Manager:         opts.ToolRunner,
		State:           opts.ToolState,
		Registry:        opts.ToolRegistry,
		Provider:        runtime.Provider(),
		DefaultProvider: true,
		Identity:        identity,
		Deps:            deps,
		MaxRounds:       opts.ToolsConfig.MaxRoundsPerTurn,
	}
	messages := &dialogue.MessageStore{Dialogues: store.Dialogues(), Gate: &dialogue.CommitGate{Sessions: opts.Sessions, Turns: opts.Turns, View: view}}
	tools.Messages = messages
	preparer := &dialogue.Preparer{Media: opts.Media, Contexts: opts.Contexts, Identity: identity, Hooks: bridge, Tools: tools}
	messages.Preparer = preparer
	calls := &dialogue.CallProcessor{Status: status, Messages: messages, Preparer: preparer, Hooks: bridge, Identity: identity, Tools: tools}
	chat := &chatroute.Loop{Status: status,
		Contexts:      opts.Contexts,
		Models:        models,
		Turns:         opts.Turns,
		View:          view,
		Preparer:      preparer,
		Tools:         tools,
		Messages:      messages,
		Caller:        &chatroute.Caller{Calls: calls},
		PromptBuilder: chatroute.PromptBuilder{System: dialogue.NewSystemPromptManager(dialogue.SoulSystemPromptSource{Soul: dialogue.StaticSoulProvider{Prompt: "test"}})},
	}
	opts.Routes.Register(routes.Binding{APIType: llm.APITypeChat, Loop: chat, Compactor: &chatroute.Compactor{Store: store, Models: models, Contexts: opts.Contexts, Loader: contextmgr.Loader{Store: store}}, Material: session.DisplayMaterial{}})
	runner := &dialogue.Runner{Status: status,
		Routes:   opts.Routes,
		Preparer: preparer,
		Messages: messages,
		Replies:  &dialogue.ReplyCommitter{Persistence: messages.Committer("append_assistant_message"), Output: output},
		Turns:    opts.Turns,
		View:     view,
	}
	execution := &ExecutionCoordinator{
		sessions:    opts.Sessions,
		sessionRows: store.Sessions(),
		turns:       opts.Turns,
		requests:    opts.Requests,
		contexts:    opts.Contexts,
		models:      models,
		dialogue:    runner,
		identity:    identity,
		view:        view,
		output:      output,
		status:      status,
		waitPolicy:  policy,
		appendWaits: newAppendWaitLifecycle(t.Context()),
	}
	connectTestForeground(t, opts.Sessions, execution.AdoptForeground)
	t.Cleanup(opts.Turns.StopAll)
	t.Cleanup(func() { _ = opts.Sessions.Close(context.Background()) })
	return &executionFixture{
		opts:          opts,
		execution:     execution,
		chat:          chat,
		runner:        runner,
		confirmations: confirmations,
		hooks:         hooks,
		out:           foregroundTurnOutput{sender: output},
	}
}

func (f *executionFixture) begin(t *testing.T) (context.Context, context.Context, dialogue.TurnInput, *turn.Execution) {
	t.Helper()
	ctx, row, err := f.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	origin := f.opts.Models.ResolveMode("work").Origin
	row, err = f.opts.Sessions.RegisterOrigin(ctx, row.ID, origin)
	if err != nil {
		t.Fatal(err)
	}
	execution := turn.NewExecution(storage.NewID())
	ctx = turn.WithAttempt(turn.WithExecution(ctx, execution), "attempt")
	if !f.opts.Turns.StartExecution(row.ID, turn.Input{Text: "hello"}, execution, "attempt") {
		t.Fatal("start attempt")
	}
	prepared, err := f.runner.PrepareTurn(ctx, dialogue.TurnInput{Session: row, Text: "hello", Input: turn.Input{Text: "hello"}, Selection: modelmgr.SelectionForTurn(ctx, f.opts.Models, row)})
	if err != nil {
		t.Fatal(err)
	}
	info, requestCtx, done := f.opts.Requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn})
	t.Cleanup(done)
	return ctx, requestCtx, dialogue.TurnInput{Session: row, Text: "hello", Selection: modelmgr.SelectionForTurn(ctx, f.opts.Models, row), RequestID: info.ID, Prepared: prepared}, execution
}

func (f *executionFixture) on(t *testing.T, point hook.Point, fn func(hook.Event) hook.Event) {
	t.Helper()
	f.hooks.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
		return fn(e), events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
	})})
}

func TestDialogueRunnerStructuredOutcomes(t *testing.T) {
	for _, name := range []string{"completed", "failed", "canceled", "paused", "superseded", "stopped"} {
		t.Run(name, func(t *testing.T) {
			model := &fakeLLM{replies: []string{"done"}}
			want := dialogue.Completed
			if name == "failed" {
				model.replies = []string{"__ERR__"}
				want = dialogue.Failed
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
						want = dialogue.Canceled
						f.opts.Requests.CancelSession(in.Session.ID)
					case "paused", "superseded":
						want = dialogue.Paused
						f.opts.Turns.InterruptLLMInput(in.Session.ID, turn.Input{Text: "append"})
						f.opts.Requests.CancelSession(in.Session.ID)
						if name == "superseded" {
							want = dialogue.Superseded
							merged, same, ok := f.opts.Turns.ResumeAppend(in.Session.ID)
							if !ok || same != execution || !f.opts.Turns.StartExecution(in.Session.ID, merged, same, "next-attempt") {
								t.Fatal("resume attempt")
							}
						}
					case "stopped":
						want = dialogue.Stopped
						f.opts.Turns.StopSession(in.Session.ID, "attempt")
						e.LLM.ToolCalls = []llm.ToolCallRequest{{ID: "stopped-tool", Name: "unused"}}
					}
					return e
				})
			}
			result := f.runner.RunTurn(ctx, requestCtx, in, f.out)
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
	f := newExecutionFixture(t, &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: "committed", Usage: &llm.Usage{TotalTokens: func() *int64 { n := int64(7); return &n }()}}}}}, newTestStore(t))
	ctx, row, err := f.execution.ResolveInput(context.Background(), "hello")
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
	if usage := f.execution.usageForSession(t.Context(), latest); usage == nil || (usage.TotalTokens == nil || *usage.TotalTokens != 7) {
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

func (s executionTimingStore) Dialogues() storage.DialogueRepository {
	return executionTimingDialogues{DialogueRepository: s.Store.Dialogues(), append: s.messages.(executionTimingMessages).append}
}

type executionTimingDialogues struct {
	storage.DialogueRepository
	append func(storage.Message) error
}

func (r executionTimingDialogues) Commit(ctx context.Context, commit storage.DialogueCommit) (events.LogResult, error) {
	for _, row := range commit.Messages {
		if err := r.append(*row); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
	}
	return r.DialogueRepository.Commit(ctx, commit)
}

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
				if len(f.opts.Requests.List()) != 1 {
					t.Fatal("material loading must run within the registered request")
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
			ctx, row, err := f.execution.ResolveInput(context.Background(), "hello")
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
