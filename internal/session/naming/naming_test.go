package naming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type textClient struct {
	llm.Client
	call func(context.Context, llm.TextRequest) (llm.TextResult, error)
}

func (textClient) RetryOptions() llm.RetryOptions { return llm.RetryOptions{} }

func (c textClient) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	return c.call(ctx, req)
}

type fixture struct {
	s         *Service
	store     *sqlite.Store
	completed chan NamingCompletedEvent
}

func newFixture(t *testing.T, step int, call func(context.Context, llm.TextRequest) (llm.TextResult, error)) *fixture {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	s, err := newTestService(Config{TriggerStep: step}, ServiceDeps{Sessions: store.Sessions(), Messages: store.Messages(), ResolveNaming: func(context.Context) modelmgr.NamingSelection {
		return modelmgr.NamingSelection{Naming: modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "provider", Model: "title"}, Client: textClient{call: call}}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: s, store: store, completed: make(chan NamingCompletedEvent, 30)}
	s.deps.Log = func(ctx context.Context, record events.LogRecord) {
		testPublishLog(ctx, record)
		if record.EventName == "session_naming_completed" && record.Category == events.LogRuntime {
			f.completed <- decodeNamingLog(t, record)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) row(t *testing.T, messages ...storage.Message) string {
	t.Helper()
	row := &storage.Session{ID: storage.NewID(), Owner: contextinfo.ActorIdentity{Platform: "test", ID: "user"}, Platform: "test", ChatID: "scope", Mode: storage.SessionModeWork, Title: "original", Metadata: `{"keep":9007199254740993}`, CreatedAt: storage.Now(), UpdatedAt: storage.Now()}
	if err := f.store.Sessions().Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		message.SessionID = row.ID
		if err := f.store.Messages().Append(t.Context(), &message); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

func (f *fixture) attempt(t *testing.T, id string) NamingCompletedEvent {
	t.Helper()
	f.s.MaybeScheduleNaming(t.Context(), id)
	e := receive(t, f.completed)
	f.idle(t, id)
	return e
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task")
		var zero T
		return zero
	}
}

func (f *fixture) idle(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.s.mu.Lock()
		state := f.s.namingStates[id]
		idle := state == nil || !state.inFlight
		f.s.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestThresholdFilteringAndPersistentCompletion(t *testing.T) {
	for _, step := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			requests := make(chan llm.TextRequest, 3)
			f := newFixture(t, step, func(_ context.Context, req llm.TextRequest) (llm.TextResult, error) {
				requests <- req
				return llm.TextResult{Text: "\n\"雪与火\"\n"}, nil
			})
			id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleSystem, Content: "ignored system"}, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "  "})
			for i := 0; i < step; i++ {
				f.s.MaybeScheduleNaming(t.Context(), id)
				if len(requests) != 0 {
					t.Fatal("scheduled below threshold")
				}
				role := storage.RoleUser
				if i == 1 {
					role = storage.RoleAssistant
				}
				if err := f.store.Messages().Append(t.Context(), &storage.Message{ID: storage.NewID(), SessionID: id, Role: role, Content: fmt.Sprintf("message%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Messages().Append(t.Context(), &storage.Message{ID: storage.NewID(), SessionID: id, Role: storage.RoleUser, Content: "excluded later"}); err != nil {
				t.Fatal(err)
			}
			e := f.attempt(t, id)
			req := receive(t, requests)
			if strings.Contains(req.Input, "excluded") || strings.Contains(req.Input, "ignored") || strings.Count(req.Input, "message") != step {
				t.Fatal(req.Input)
			}
			if e.ResultStatus != events.ResultSucceeded || *e.MessageCount != step {
				t.Fatalf("event=%+v", e)
			}
			row, err := f.store.Sessions().Get(t.Context(), id)
			if err != nil || row.Title != "雪与火" {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			fields, _ := storage.DecodeSessionMetadata(row.Metadata)
			if string(fields["keep"]) != "9007199254740993" || string(fields["title_renamed"]) != "true" || string(fields["title_source"]) != `"auto"` {
				t.Fatal(row.Metadata)
			}
			f.s.ForgetSession(id)
			f.s.MaybeScheduleNaming(t.Context(), id)
			if len(requests) != 0 {
				t.Fatal("completed session renamed after eviction")
			}
			if err := f.s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted, err := newTestService(Config{TriggerStep: step}, f.s.deps)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close(context.Background())
			if err := restarted.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted.MaybeScheduleNaming(t.Context(), id)
			if len(requests) != 0 {
				t.Fatal("completed session renamed after restart")
			}
		})
	}
}

type faultSessions struct {
	storage.SessionRepository
	err       error
	mutations atomic.Int32
}

func (r *faultSessions) Mutate(ctx context.Context, id string, fn func(*storage.Session) error) (*storage.Session, error) {
	r.mutations.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	return r.SessionRepository.Mutate(ctx, id, fn)
}

func TestThreeFailuresThenFallbackAndPersistenceRetry(t *testing.T) {
	for _, output := range []string{"", "New session", "model error", "model timeout"} {
		t.Run(output, func(t *testing.T) {
			var calls atomic.Int32
			original := errors.New("gateway failure")
			f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
				calls.Add(1)
				if output == "model timeout" {
					return llm.TextResult{}, context.DeadlineExceeded
				}
				if output == "model error" {
					return llm.TextResult{Text: "partial"}, original
				}
				return llm.TextResult{Text: output}, nil
			})
			id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleAssistant, Content: "first assistant"}, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "fallback text"})
			repo := &faultSessions{SessionRepository: f.store.Sessions(), err: errors.New("write failed")}
			f.s.deps.Sessions = repo
			for i := 1; i <= 3; i++ {
				e := f.attempt(t, id)
				if e.FailureCount != i {
					t.Fatalf("event=%+v", e)
				}
				row, _ := f.store.Sessions().Get(t.Context(), id)
				if row.Title != "original" {
					t.Fatalf("premature fallback: %+v", row)
				}
				if i < 3 && repo.mutations.Load() != 0 {
					t.Fatal("persisted before third failure")
				}
				if i == 3 && (!sameNamingError(e.FallbackError, repo.err) || e.FallbackTitle != "fallback text") {
					t.Fatalf("event=%+v", e)
				}
				if output == "model error" && (!sameNamingError(e.GenerationError, original)) {
					t.Fatalf("lost generation failure: %+v", e)
				}
			}
			repo.err = nil
			e := f.attempt(t, id)
			if e.FallbackResultStatus != events.ResultSucceeded || e.GenerationResultStatus != events.ResultUnspecified || calls.Load() != 3 {
				t.Fatalf("retry=%+v calls=%d", e, calls.Load())
			}
			row, _ := f.store.Sessions().Get(t.Context(), id)
			fields, _ := storage.DecodeSessionMetadata(row.Metadata)
			if row.Title != "fallback text" || string(fields["title_source"]) != `"fallback"` || string(fields["title_renamed"]) != "true" {
				t.Fatalf("row=%+v", row)
			}
			f.s.ForgetSession(id)
			f.s.MaybeScheduleNaming(t.Context(), id)
			if calls.Load() != 3 {
				t.Fatal("fallback completion was not persistent")
			}
		})
	}
}

func TestStorageFailureDoesNotConsumeGenerationRetries(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		return llm.TextResult{Text: "generated"}, nil
	})
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
	repo := &faultSessions{SessionRepository: f.store.Sessions(), err: errors.New("storage unavailable")}
	f.s.deps.Sessions = repo
	for i := 0; i < 4; i++ {
		e := f.attempt(t, id)
		if e.FailureCount != 0 || !sameNamingError(e.PersistenceError, repo.err) || e.FallbackResultStatus != events.ResultUnspecified {
			t.Fatalf("event=%+v", e)
		}
	}
	repo.err = nil
	if e := f.attempt(t, id); e.ResultStatus != events.ResultSucceeded {
		t.Fatalf("retry=%+v", e)
	}
}

func TestManualRenameWinsAndMalformedMetadataFailsPreparation(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		started <- struct{}{}
		<-release
		return llm.TextResult{Text: "generated"}, nil
	})
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
	f.s.MaybeScheduleNaming(t.Context(), id)
	receive(t, started)
	_, err := f.store.Sessions().Mutate(t.Context(), id, func(row *storage.Session) error {
		row.Title = "manual"
		row.Metadata = `{"title_renamed":true,"title_source":"manual"}`
		return nil
	})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	e := receive(t, f.completed)
	f.idle(t, id)
	if e.ResultStatus != events.ResultSkipped || *e.Title != "manual" || e.FallbackResultStatus != events.ResultUnspecified {
		t.Fatalf("event=%+v", e)
	}
	for _, metadata := range []string{`{`, `{"title_renamed":"bad"}`, `null`} {
		id := f.row(t)
		_, err := f.store.Sessions().Mutate(t.Context(), id, func(row *storage.Session) error { row.Metadata = metadata; return nil })
		if err != nil {
			t.Fatal(err)
		}
		e := f.attempt(t, id)
		if e.PreparationError == nil || e.FailureCount != 0 || e.GenerationResultStatus != events.ResultUnspecified {
			t.Fatalf("event=%+v", e)
		}
	}
}

func TestNoUserFallbackKeepsOriginalAndDoesNotComplete(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		calls.Add(1)
		return llm.TextResult{}, errors.New("model error")
	})
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleAssistant, Content: "assistant only"})
	for i := 0; i < 4; i++ {
		f.attempt(t, id)
	}
	row, _ := f.store.Sessions().Get(t.Context(), id)
	if calls.Load() != 3 || row.Title != "original" || strings.Contains(row.Metadata, "title_renamed") {
		t.Fatalf("row=%+v calls=%d", row, calls.Load())
	}
	if err := f.store.Messages().Append(t.Context(), &storage.Message{ID: storage.NewID(), SessionID: id, Role: storage.RoleUser, Content: "late user"}); err != nil {
		t.Fatal(err)
	}
	if e := f.attempt(t, id); e.FallbackResultStatus != events.ResultSucceeded || calls.Load() != 3 {
		t.Fatalf("event=%+v", e)
	}
}

func TestProtectedTitlesAndMissingRows(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		t.Error("protected title invoked model")
		return llm.TextResult{}, nil
	})
	for _, source := range []string{"manual", "cron", "compact", "auto", "fallback"} {
		id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
		_, err := f.store.Sessions().Mutate(t.Context(), id, func(row *storage.Session) error {
			row.Metadata = fmt.Sprintf(`{"title_renamed":true,"title_source":%q}`, source)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		f.s.MaybeScheduleNaming(t.Context(), id)
	}
	e := f.attempt(t, "missing")
	if !sameNamingError(e.PreparationError, storage.ErrNotFound) {
		t.Fatalf("event=%+v", e)
	}
	f.s.mu.Lock()
	_, exists := f.s.namingStates["missing"]
	f.s.mu.Unlock()
	if exists {
		t.Fatal("missing session retained cache")
	}
}

func TestNormalizeTitleUnicode(t *testing.T) {
	text := strings.Repeat("雪", 41)
	if got := normalizeTitle(text); got != strings.Repeat("雪", 40)+"..." {
		t.Fatal(got)
	}
	for _, text := range []string{"", " \n ", "\"\"", "New session", "new SESSION", "\" \t \""} {
		if invalidTitleReason(normalizeTitle(text)) == "" {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestPreparationFailureIdentityAndRetry(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		return llm.TextResult{Text: "retry title"}, nil
	})
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
	original := errors.New("read messages failed")
	f.s.deps.Messages = messageFunc(func(context.Context, string) ([]storage.Message, error) { return nil, original })
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: "wrong", RequestID: "request", RunID: "run"})
	f.s.MaybeScheduleNaming(ctx, id)
	e := receive(t, f.completed)
	if e.MessageCount != nil || !sameNamingError(e.PreparationError, original) || e.FailureCount != 0 || e.Identity.SessionID != id || e.Identity.RequestID != "request" {
		t.Fatalf("event=%+v", e)
	}
	f.s.deps.Messages = f.store.Messages()
	if e := f.attempt(t, id); e.ResultStatus != events.ResultSucceeded {
		t.Fatalf("retry=%+v", e)
	}
}

type messageFunc func(context.Context, string) ([]storage.Message, error)

func (fn messageFunc) ListBySession(ctx context.Context, id string) ([]storage.Message, error) {
	return fn(ctx, id)
}

func TestSourceDeletedWhileGeneratingDoesNotRetainCache(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		close(started)
		<-release
		return llm.TextResult{Text: "late title"}, nil
	})
	t.Cleanup(unblock)
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
	f.s.MaybeScheduleNaming(t.Context(), id)
	receive(t, started)
	if err := f.store.Sessions().Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	unblock()
	e := receive(t, f.completed)
	f.idle(t, id)
	if !sameNamingError(e.PersistenceError, storage.ErrNotFound) || e.FailureCount != 0 {
		t.Fatalf("event=%+v", e)
	}
	f.s.mu.Lock()
	_, exists := f.s.namingStates[id]
	f.s.mu.Unlock()
	if exists {
		t.Fatal("deleted session retained state")
	}
}

func testPublishLog(ctx context.Context, record events.LogRecord) {
	events.Log(context.WithoutCancel(ctx), record)
}
func newTestService(cfg Config, deps ServiceDeps) (*Service, error) {
	if deps.Log == nil {
		deps.Log = testPublishLog
	}
	return NewService(cfg, deps), nil
}
func sameNamingError(a, b error) bool {
	return a != nil && b != nil && strings.Contains(a.Error(), b.Error())
}
func decodeNamingLog(t *testing.T, r events.LogRecord) NamingCompletedEvent {
	t.Helper()
	var data struct {
		Provider                                       string
		Model                                          string
		FailureCount                                   int  `json:"failure_count"`
		MessageCount                                   *int `json:"message_count"`
		Title                                          *string
		Preparation, Generation, Persistence, Fallback struct {
			ResultStatus string `json:"result_status"`
			Error        string
			Title        string
		}
		Cancellation string
	}
	if err := json.Unmarshal([]byte(r.Content), &data); err != nil {
		t.Fatal(err)
	}
	status := func(s string) events.ResultStatus {
		for _, v := range []events.ResultStatus{events.ResultSucceeded, events.ResultFailed, events.ResultCanceled, events.ResultRejected, events.ResultSkipped} {
			if v.String() == s {
				return v
			}
		}
		return events.ResultUnspecified
	}
	failure := func(s string) error {
		if s == "" {
			return nil
		}
		return errors.New(s)
	}
	return NamingCompletedEvent{Identity: r.Identity, ResultStatus: r.ResultStatus, Provider: data.Provider, Model: data.Model, FailureCount: data.FailureCount, MessageCount: data.MessageCount, Title: data.Title,
		PreparationError: failure(data.Preparation.Error), GenerationError: failure(data.Generation.Error), PersistenceError: failure(data.Persistence.Error), FallbackError: failure(data.Fallback.Error),
		PreparationResultStatus: status(data.Preparation.ResultStatus), GenerationResultStatus: status(data.Generation.ResultStatus), PersistenceResultStatus: status(data.Persistence.ResultStatus), FallbackResultStatus: status(data.Fallback.ResultStatus), FallbackTitle: data.Fallback.Title, CancellationError: failure(data.Cancellation)}
}
