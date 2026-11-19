package responses

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type archiveRepository struct {
	storage.DialogueRepository
	err               error
	created, finished int
	failure           string
	cleanupErr        error
}

func (r *archiveRepository) CreateExchange(_ context.Context, exchange *storage.NativeExchange) error {
	r.created++
	exchange.ID = "exchange"
	return nil
}

func (r *archiveRepository) FinishExchange(ctx context.Context, _, _, _, _, failure string) error {
	r.finished++
	r.failure, r.cleanupErr = failure, ctx.Err()
	return r.err
}

type archiveGate struct{}

func (archiveGate) Enter(ctx context.Context, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

type archiveClient struct {
	*api.Client
	err    error
	cancel context.CancelFunc
}

func (c archiveClient) StreamPrepared(context.Context, api.PreparedRequest) (<-chan api.Event, error) {
	c.cancel()
	if c.err != nil {
		return nil, c.err
	}
	stream := make(chan api.Event, 1)
	stream <- api.Event{Type: "response.completed", Response: &api.Response{}}
	close(stream)
	return stream, nil
}

func (c archiveClient) CompactPrepared(context.Context, api.PreparedCompactRequest) (*api.CompactResult, error) {
	c.cancel()
	return &api.CompactResult{Response: &api.Response{}}, c.err
}

func TestArchiveFailureDoesNotReplaceRequestFailure(t *testing.T) {
	requestErr, saveErr := errors.New("provider original failure"), errors.New("archive unavailable")
	for _, route := range []string{"native", "compact"} {
		for _, tc := range []struct {
			name       string
			requestErr error
			cancel     bool
		}{
			{"request succeeded", nil, false},
			{"request failed", requestErr, false},
			{"request failed during cancellation", requestErr, true},
			{"request canceled", context.Canceled, true},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{SessionID: "session", RequestID: "request"})
				base, err := api.New("http://unused.invalid", "", nil, nil, api.RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				client := archiveClient{Client: base, err: tc.requestErr, cancel: func() {}}
				if tc.cancel {
					client.cancel = cancel
				}
				repo := &archiveRepository{err: saveErr}
				row := &storage.Session{ID: "session"}
				selection := modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "test", Model: "model"}, Origin: llm.Origin{APIType: llm.APITypeResponse, Provider: "test", BaseURL: "http://unused.invalid"}}
				logs := make(chan events.LogRecord, 2)
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
					if record.EventName == route+"_archive_failed" {
						logs <- record
					}

				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Disconnect()
				if route == "compact" {
					compactor := &Compactor{Context: &Context{Repository: repo}, Gate: archiveGate{}}
					_, err = compactor.compactOnce(ctx, row, selection, client, api.PreparedCompactRequest{}, "", "{}")
				} else {
					state := &turnState{ctx: ctx, session: row, selection: selection, route: &Loop{LoopDeps: LoopDeps{Repository: repo, Gate: archiveGate{}, View: dialogue.ExecutionView{}}}}
					stream := delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) { return delivery.Receipt{}, nil })
					_, err = state.nativeCall(ctx, client, api.Request{Model: "model"}, nil, stream, new(dialogue.CallProcessor).Begin(row.ID, selection))
				}
				want := tc.requestErr
				if want == nil {
					want = saveErr
				}
				if !errors.Is(err, want) || repo.created != 1 || repo.finished != 1 || repo.cleanupErr != nil {
					t.Fatalf("err=%v archive=%+v", err, repo)
				}
				if tc.requestErr == nil {
					if repo.failure != "" || len(logs) != 0 {
						t.Fatal("terminal archive failure was reported below task boundary", repo.failure, len(logs))
					}
					return
				}
				if errors.Is(err, saveErr) || repo.failure != tc.requestErr.Error() {
					t.Fatal("archive replaced request error", err, repo.failure)
				}
				select {
				case record := <-logs:
					if !strings.Contains(record.Content, saveErr.Error()) || strings.Contains(record.Content, tc.requestErr.Error()) || record.Identity.RequestID != "request" {
						t.Fatalf("independent archive diagnostic=%+v", record)
					}
				case <-time.After(time.Second):
					t.Fatal("secondary archive failure was not reported")
				}
			})
		}
	}
}

type eventClient struct {
	*api.Client
	events []api.Event
}

func (c eventClient) StreamPrepared(context.Context, api.PreparedRequest) (<-chan api.Event, error) {
	ch := make(chan api.Event, len(c.events))
	for _, e := range c.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func streamFixture(t testing.TB, events []api.Event) (*turnState, eventClient) {
	t.Helper()
	client, err := api.New("http://unused.invalid", "", nil, nil, api.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &turnState{ctx: t.Context(), session: &storage.Session{ID: "session"}, route: &Loop{LoopDeps: LoopDeps{Repository: &archiveRepository{}, Gate: archiveGate{}}}}, eventClient{client, events}
}

func TestPartialStreamDeliveryAndArchive(t *testing.T) {
	failure := errors.New("interrupted")
	for _, success := range []bool{false, true} {
		events := []api.Event{
			{Type: "response.content_part.added", OutputIndex: 9000, ContentIndex: 23, Raw: json.RawMessage(`{"part":{"type":"output_text","text":"","extra":"keep"}}`)},
			{Type: "response.output_text.delta", OutputIndex: 9000, ContentIndex: 23, Delta: "a"},
			{Type: "response.output_text.delta", OutputIndex: 9000, ContentIndex: 23, Delta: "b"},
			{Type: "response.output_text.done", OutputIndex: 9000, ContentIndex: 23, Text: "AB"},
			{Type: "response.refusal.delta", OutputIndex: 9000, ContentIndex: 4, Delta: "n"},
		}
		wantEvents := []string{"text_delta:a", "text_delta:b", "text_delta:n"}
		if success {
			events = append(events, api.Event{Type: "response.completed", Response: &api.Response{Status: "completed"}})
			wantEvents = append(wantEvents, "finish:")
		} else {
			events = append(events, api.Event{Error: failure})
		}
		s, c := streamFixture(t, events)
		var got []string
		stream := delivery.StreamFunc(func(_ context.Context, e delivery.StreamEvent) (delivery.Receipt, error) {
			got = append(got, string(e.Type)+":"+e.Text)
			return delivery.Receipt{}, nil
		})
		r, err := s.nativeCall(t.Context(), c, api.Request{Model: "model"}, nil, stream, new(dialogue.CallProcessor).Begin("session", s.selection))
		if (success && err != nil) || (!success && !errors.Is(err, failure)) || !reflect.DeepEqual(got, wantEvents) || len(r.Items) != 1 {
			t.Fatalf("success=%v events=%v items=%v err=%v", success, got, r.Items, err)
		}
		var item struct{ Content []map[string]string }
		if err := json.Unmarshal(r.Items[0].Raw, &item); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(item.Content, []map[string]string{{"type": "refusal", "refusal": "n"}, {"type": "output_text", "text": "AB", "extra": "keep"}}) {
			t.Fatal(item)
		}
	}
}

func TestItemDoneReplacesBufferedDeltas(t *testing.T) {
	snapshot, err := api.ParseItem([]byte(`{"type":"message","role":"assistant","extra":"keep","content":[{"type":"output_text","text":"snapshot"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := api.ParseItem([]byte(`{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"terminal"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{false, true} {
		events := []api.Event{{Type: "response.output_text.delta", Delta: "old"}, {Type: "response.output_item.done", Item: &snapshot}}
		failure := errors.New("disconnected")
		wantText := "snapshot"
		if success {
			events = append(events, api.Event{Type: "response.completed", Response: &api.Response{Status: "completed", Output: []api.Item{terminal}}})
			wantText = "terminal"
		} else {
			events = append(events, api.Event{Error: failure})
		}
		s, c := streamFixture(t, events)
		stream := delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) { return delivery.Receipt{}, nil })
		r, err := s.nativeCall(t.Context(), c, api.Request{Model: "model"}, nil, stream, new(dialogue.CallProcessor).Begin("session", s.selection))
		if (success && err != nil) || (!success && !errors.Is(err, failure)) || len(r.Items) != 1 || outputText(r.Items) != wantText {
			t.Fatalf("items=%v err=%v", r.Items, err)
		}
		var fields struct{ Extra string }
		if err := json.Unmarshal(r.Items[0].Raw, &fields); err != nil || fields.Extra != "keep" {
			t.Fatalf("snapshot metadata: %s", r.Items[0].Raw)
		}
	}
}

func BenchmarkNativeCallDeltas(b *testing.B) {
	events := make([]api.Event, 513)
	for i := range 512 {
		events[i] = api.Event{Type: "response.output_text.delta", Delta: "abcdefghijklmnop"}
	}
	events[512] = api.Event{Type: "response.completed", Response: &api.Response{Status: "completed"}}
	s, c := streamFixture(b, events)
	stream := delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) { return delivery.Receipt{}, nil })
	b.ReportAllocs()
	for b.Loop() {
		_, err := s.nativeCall(b.Context(), c, api.Request{Model: "model"}, nil, stream, new(dialogue.CallProcessor).Begin("session", s.selection))
		if err != nil {
			b.Fatal(err)
		}
	}
}
