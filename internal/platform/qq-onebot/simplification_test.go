package qqonebot

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
)

func TestLogIdentityPreservesExecutionFields(t *testing.T) {
	if got := logIdentity(context.Background()); !reflect.DeepEqual(got, events.LogIdentity{}) {
		t.Fatalf("missing context: %+v", got)
	}
	facts := contextinfo.Execution{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
	got := logIdentity(contextinfo.WithExecution(t.Context(), facts))
	want := events.LogIdentity{SessionID: &facts.SessionID, RunID: &facts.RunID, Attempt: &facts.Attempt, RequestID: &facts.RequestID, RootRequestID: &facts.RootRequestID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestDispatchResponseDecodeBoundaries(t *testing.T) {
	transport := newTransport("", "", 0)
	defer transport.Close(context.Background())
	ch := make(chan response, 1)
	transport.pending["known"] = ch
	for _, tc := range []struct {
		raw     string
		handled bool
	}{
		{"{", false}, {"{}", false}, {`{"echo":12}`, false},
		{`{"echo":"known","retcode":"bad"}`, false},
		{`{"echo":"unknown","status":"ok"}`, true},
		{`{"echo":"known","status":"ok","data":{"message_id":123}}`, true},
		{`{"echo":"known","status":"ok"}`, true},
	} {
		if got := transport.dispatchResponse(json.RawMessage(tc.raw)); got != tc.handled {
			t.Fatalf("%s: handled=%v", tc.raw, got)
		}
	}
	if len(transport.pending) != 0 || len(ch) != 1 {
		t.Fatal("pending or duplicate response")
	}
	if got := <-ch; string(got.Data) != `{"message_id":123}` {
		t.Fatalf("response changed: %+v", got)
	}
}
