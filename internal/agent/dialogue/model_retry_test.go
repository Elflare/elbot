package dialogue

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type retryCallClient struct {
	llm.Client
	options llm.RetryOptions
}

func (c retryCallClient) RetryOptions() llm.RetryOptions { return c.options }

func TestCallRetryStreamBoundary(t *testing.T) {
	terminal := errors.New("terminal")
	resetFailure := errors.New("reset failed")
	for _, tc := range []struct {
		name       string
		maxRetries int
		wantSteps  []string
		wantErr    error
	}{
		{"success", 2, []string{"prepare", "attempt"}, nil},
		{"retry", 2, []string{"prepare", "attempt", "reset", "attempt"}, nil},
		{"terminal", 2, []string{"prepare", "attempt"}, terminal},
		{"exhausted", 1, []string{"prepare", "attempt", "reset", "attempt"}, io.ErrUnexpectedEOF},
		{"cancel", 2, []string{"prepare", "attempt"}, context.Canceled},
		{"cancel waiting", 2, []string{"prepare", "attempt"}, context.Canceled},
		{"reset fails", 2, []string{"prepare", "attempt", "reset"}, resetFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var steps []string
			client := retryCallClient{options: llm.RetryOptions{MaxRetries: tc.maxRetries, RetryInitialDelay: time.Nanosecond}}
			client.options.OnRetry = func(got context.Context, e llm.RetryEvent) {
				if got != ctx || !errors.Is(e.Err, io.ErrUnexpectedEOF) {
					t.Fatalf("retry=%+v", e)
				}
				if tc.name == "cancel waiting" {
					cancel()
				}
			}
			stream := delivery.StreamFunc(func(_ context.Context, e delivery.StreamEvent) (delivery.Receipt, error) {
				if e.Type != delivery.StreamReset {
					t.Fatalf("unexpected event=%s", e.Type)
				}
				steps = append(steps, "reset")
				if tc.name == "reset fails" {
					return delivery.Receipt{}, resetFailure
				}
				return delivery.Receipt{}, nil
			})
			processor := CallProcessor{Hooks: callOnlyHooks{}, Tools: &ToolExecutor{Manager: hookTools{}}}
			calls := 0
			result, err := processor.Call(ctx, &storage.Session{ID: "s"}, modelmgr.Selection{Client: client},
				nil, nil, nil, nil, stream,
				func(context.Context, agentshare.CallInput) error { steps = append(steps, "prepare"); return nil },
				func(context.Context, agentshare.CallObservation) (agentshare.CallOutput, error) {
					calls++
					steps = append(steps, "attempt")
					out := agentshare.CallOutput{SourceText: "text"}
					switch tc.name {
					case "success":
						return out, nil
					case "terminal":
						return out, terminal
					case "cancel":
						cancel()
						return out, context.Canceled
					case "retry":
						if calls == 2 {
							return out, nil
						}
					}
					return out, llm.MapStreamError(io.ErrUnexpectedEOF)
				})
			if !errors.Is(err, tc.wantErr) || !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Fatalf("steps=%v error=%v", steps, err)
			}
			if err == nil && result.Text != "text" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
