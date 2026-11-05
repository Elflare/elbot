package chat

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
)

type canceledErrorClient struct {
	chatcompletions.Streamer
	cancel context.CancelFunc
	err    error
}

func (c canceledErrorClient) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	c.cancel()
	return nil, c.err
}

func TestCallPreservesIndependentFailureDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := errors.New("original request failure")
	observation := new(dialogue.CallProcessor).Begin("session", modelmgr.Selection{})
	stream := delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) {
		t.Fatal("failed request must not emit output")
		return delivery.Receipt{}, nil
	})
	_, err := new(Caller).callOnce(ctx, canceledErrorClient{cancel: cancel, err: failure}, chatcompletions.Request{}, observation, stream, false)
	if err != failure || observation.Event().Err != failure || !observation.Event().ProviderError {
		t.Fatalf("cancellation replaced request failure: %v %+v", err, observation.Event())
	}
}
