package chat

import (
	"context"
	"errors"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm/chatcompletions"
)

type reasoningStreamer struct {
	chatcompletions.Streamer
	chunks []chatcompletions.Chunk
}

func (s reasoningStreamer) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch := make(chan chatcompletions.Chunk, len(s.chunks))
	for _, chunk := range s.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

type callObservationStub struct {
	event agentshare.ModelCallCompletedEvent
	log   events.LogRecord
}

func (s *callObservationStub) Event() *agentshare.ModelCallCompletedEvent { return &s.event }
func (s *callObservationStub) LogRecord() *events.LogRecord               { return &s.log }
func (*callObservationStub) Publish(context.Context, error)               {}

func TestCallOnceCollectsReasoningDeltasForHistory(t *testing.T) {
	caller := &Caller{}
	observation := &callObservationStub{}
	stream := delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) {
		return delivery.Receipt{}, nil
	})
	result, err := caller.callOnce(t.Context(), reasoningStreamer{chunks: []chatcompletions.Chunk{
		{DeltaReasoningContent: "先检查"},
		{DeltaReasoningContent: "再回答"},
		{DeltaContent: "完成"},
	}}, chatcompletions.Request{}, observation, stream, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReasoningContent != "先检查再回答" || result.Text != "完成" {
		t.Fatalf("call result = %+v", result)
	}
}

func TestCallOnceReturnsPartialReasoningOnlyWithFailedAttempt(t *testing.T) {
	want := errors.New("provider failed")
	caller := &Caller{}
	result, err := caller.callOnce(t.Context(), reasoningStreamer{chunks: []chatcompletions.Chunk{
		{DeltaReasoningContent: "partial"},
		{Error: want},
	}}, chatcompletions.Request{}, &callObservationStub{}, delivery.StreamFunc(func(context.Context, delivery.StreamEvent) (delivery.Receipt, error) {
		return delivery.Receipt{}, nil
	}), false)
	if !errors.Is(err, want) || result.ReasoningContent != "partial" {
		t.Fatalf("failed attempt = %+v, err=%v", result, err)
	}
}
