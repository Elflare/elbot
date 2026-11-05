package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"errors"
	"testing"
)

func TestNotifyPreservesIndependentFailuresAndCancellation(t *testing.T) {
	fault := errors.New("plugin failed")
	var records []events.LogRecord
	manager := NewManager(func(_ context.Context, r events.LogRecord) { records = append(records, r) }, nil)
	for i, cause := range []error{context.Canceled, fault, nil} {
		manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: i, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, error) { return event, cause })})
	}
	err := manager.Notify(t.Context(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
	if err != nil || len(records) != 3 {
		t.Fatalf("err=%v records=%+v", err, records)
	}
	for i, want := range []events.ResultStatus{events.ResultCanceled, events.ResultFailed, events.ResultSucceeded} {
		if records[i].ResultStatus != want {
			t.Fatalf("record %d=%+v", i, records[i])
		}
	}
}
