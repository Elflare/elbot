package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type nativeCommitFaultStore struct {
	storage.Store
	fault func(storage.DialogueCommit) error
}

func (s nativeCommitFaultStore) Dialogues() storage.DialogueRepository {
	return nativeCommitFaultRepository{DialogueRepository: s.Store.Dialogues(), fault: s.fault}
}

type nativeCommitFaultRepository struct {
	storage.DialogueRepository
	fault func(storage.DialogueCommit) error
}

func (r nativeCommitFaultRepository) Commit(ctx context.Context, commit storage.DialogueCommit) error {
	if err := r.fault(commit); err != nil {
		return err
	}
	return r.DialogueRepository.Commit(ctx, commit)
}

func TestResponsesFirstCommitFailureCanContinueWithoutReprocessingOldInput(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var inputHooks atomic.Int32
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMTurnPrepared, Name: "count-input", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
		inputHooks.Add(1)
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 1 && (request.PreviousResponseID != "" || len(request.Input) != 2) {
			t.Errorf("retry=%+v", request)
		}
		emitNative(w, "response", "completed", nativeText("reply"))
	}, func(opts *testAgentOptions) {
		opts.HookManager = manager
		opts.Store = nativeCommitFaultStore{Store: opts.Store, fault: func(commit storage.DialogueCommit) error {
			if commit.Native != nil && commit.Native.Checkpoint.ID != "" && fail.Swap(false) {
				return errors.New("checkpoint write failed")
			}
			return nil
		}}
	})
	if err := f.agent.HandleMessage(t.Context(), "first input"); err == nil || !strings.Contains(err.Error(), "checkpoint write failed") {
		t.Fatalf("first error=%v", err)
	}
	if err := f.agent.HandleMessage(t.Context(), "second input"); err != nil {
		t.Fatal(err)
	}
	if inputHooks.Load() != 2 || len(f.captured()) != 2 {
		t.Fatalf("input hooks=%d requests=%d", inputHooks.Load(), len(f.captured()))
	}
}

func TestResponsesPairCommitFailureKeepsUnknownNativeOutcomeAndStopsBatch(t *testing.T) {
	var executions atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "once", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		executions.Add(1)
		return &tool.Result{Content: "uncommitted result"}, nil
	}})
	var fail atomic.Bool
	fail.Store(true)
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "head", "completed", nativeCall("one", "once", `{}`), nativeCall("two", "once", `{}`))
			return
		}
		body := inputJSON(request)
		if request.PreviousResponseID != "head" || !strings.Contains(body, "outcome is unknown") || !strings.Contains(body, "was not executed") || strings.Contains(body, "uncommitted result") {
			t.Errorf("unsafe continuation=%+v", request)
		}
		emitNative(w, "continued", "completed", nativeText("done"))
	}, func(opts *testAgentOptions) {
		opts.ToolRegistry = registry
		opts.Store = nativeCommitFaultStore{Store: opts.Store, fault: func(commit storage.DialogueCommit) error {
			if commit.ToolPair != nil && fail.Swap(false) {
				return errors.New("pair write failed")
			}
			return nil
		}}
	})
	if err := f.agent.HandleMessage(t.Context(), "@tool:once run"); err == nil || !strings.Contains(err.Error(), "pair write failed") {
		t.Fatalf("first error=%v", err)
	}
	if executions.Load() != 1 {
		t.Fatalf("batch continued after commit failure: %d", executions.Load())
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 1 {
		t.Fatalf("historical tool replayed: %d", executions.Load())
	}
	rows, err := f.store.Messages().ListBySession(t.Context(), fixtureSession(t, f).ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Role == storage.RoleTool || strings.Contains(row.Metadata, "tool_calls") {
			t.Fatalf("unpaired/fictional display history=%+v", rows)
		}
	}
}

func TestResponsesRejectsDisplayHistoryWithoutCanonicalQueuedInput(t *testing.T) {
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		emitNative(w, "unexpected", "completed", nativeText("unexpected"))
	})
	row, err := f.agent.execution.sessions.Create(t.Context(), f.agent.Scope(t.Context()), session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.execution.sessions.RegisterOrigin(t.Context(), row.ID, f.agent.execution.models.ProviderOrigins()[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Messages().Append(t.Context(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "missing original native input"}); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err == nil || !strings.Contains(err.Error(), "缺少完整原生") {
		t.Fatalf("missing input accepted: %v", err)
	}
	if len(f.captured()) != 0 {
		t.Fatal("called provider with incomplete history")
	}
}

func TestResponsesFailedFirstForegroundRequestRetainsOneTailNotice(t *testing.T) {
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		body := inputJSON(request)
		if strings.Count(body, "[系统提示]") != 1 || !strings.Contains(string(request.Input[len(request.Input)-1]), "[系统提示]") || strings.Contains(request.Instructions, "[系统提示]") {
			t.Errorf("wrong notice placement: %+v", request)
		}
		if index == 0 {
			emitNative(w, "partial", "incomplete", nativeText("partial"))
			return
		}
		if len(request.Input) != 3 {
			t.Errorf("queued inputs=%+v", request)
		}
		emitNative(w, "recovered", "completed", nativeText("done"))
	})
	scope := f.agent.Scope(t.Context())
	row, err := f.agent.execution.sessions.PrepareBackground(t.Context(), scope, session.BackgroundRequest{Kind: "cron", Name: "failed-first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.execution.sessions.RegisterOrigin(t.Context(), row.ID, f.agent.execution.models.ProviderOrigins()[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.execution.sessions.Resume(t.Context(), scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "first"); err == nil {
		t.Fatal("partial request completed")
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("display history=%+v %v", rows, err)
	}
}
