package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type failCompletedEventRepository struct {
	storage.ElnisEventRepository
	err error
}

func (r failCompletedEventRepository) Update(ctx context.Context, req storage.UpdateElnisEventRequest) error {
	if req.Status == StatusCompleted {
		return r.err
	}
	return r.ElnisEventRepository.Update(ctx, req)
}

func TestElnisLogsMediaFailureAndSuccessfulRunSaveFailure(t *testing.T) {
	for _, kind := range []string{"media", "completion_save"} {
		t.Run(kind, func(t *testing.T) {
			runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":"done"}`}
			service, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
				return delivery.Receipt{}, nil
			})
			defer cleanup()
			var queued QueuedLLMEvent
			service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
			req := testRequest(ModeLLM)
			if _, err := service.Handle(context.Background(), "secret", req); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("completion state save failed")
			if kind == "completion_save" {
				service.store = elnisRepositoryStore{Store: service.store, repo: failCompletedEventRepository{ElnisEventRepository: service.store.ElnisEvents(), err: failure}}
			} else {
				service.media = nil
				queued.Event.Request.Segments = []Segment{{Kind: SegmentKindImage, URL: "https://example.invalid/image.png"}}
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Connect(func(_ context.Context, record events.LogRecord) error {
				if record.Category == events.LogElnis && record.EventName == "elnis_llm_failed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			err = service.RunLLMEvent(context.Background(), queued.Event, queued.EventID)
			if err == nil || len(records) != 1 || records[0].ResultStatus != events.ResultFailed {
				t.Fatalf("error=%v logs=%#v", err, records)
			}
			var content map[string]json.RawMessage
			if err := json.Unmarshal([]byte(records[0].Content), &content); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"elwisp_name": req.Elwisp.Name, "source": req.Source, "source_id": req.ID, "mode": req.Mode, "event_id": queued.EventID} {
				var got string
				if err := json.Unmarshal(content[key], &got); err != nil || got != want {
					t.Fatalf("%s=%s error=%v", key, content[key], err)
				}
			}
			if string(content["tags"]) != `["test"]` || len(content["event_key"]) == 0 {
				t.Fatalf("query facts lost: %s", records[0].Content)
			}
			if kind == "completion_save" {
				if !errors.Is(err, failure) || !strings.Contains(string(content["persistence_error"]), failure.Error()) || !strings.Contains(string(content["result"]), `"completed":true`) || len(runner.requests) != 1 {
					t.Fatalf("completed facts lost: %s error=%v", records[0].Content, err)
				}
			} else {
				if len(runner.requests) != 0 || len(content["error"]) == 0 || len(content["persistence_error"]) != 0 {
					t.Fatalf("media failure lost: %s", records[0].Content)
				}
				row, err := service.store.ElnisEvents().Get(context.Background(), queued.EventID)
				if err != nil || row.Status != StatusFailed {
					t.Fatalf("failure state not saved: %+v error=%v", row, err)
				}
			}
		})
	}
}
