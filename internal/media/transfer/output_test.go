package transfer

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/events"
	"elbot/internal/media/share"
	"elbot/internal/operationresult"
)

func TestOutputSigningFailureReleasesHold(t *testing.T) {
	failure := errors.New("sign failed")
	h := &fakeHeld{sign: func(context.Context) (string, error) { return "", failure }}
	m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
		return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: h}, nil
	}}, Options{Log: func(context.Context, events.LogRecord) {}})
	_, usageID, err := m.ResolveForOutput(t.Context(), "one")
	if !errors.Is(err, failure) || usageID != "" || h.closes.Load() != 1 || h.opens.Load() != 0 {
		t.Fatalf("usage=%q err=%v closes=%d opens=%d", usageID, err, h.closes.Load(), h.opens.Load())
	}
}
