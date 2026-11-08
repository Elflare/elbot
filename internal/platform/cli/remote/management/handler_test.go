package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/control"
)

type handlerControlStub struct {
	ControlCapabilities
	result control.RespondConfirmationResult
}

func (s handlerControlStub) RespondConfirmation(context.Context, control.RespondConfirmationRequest) (control.RespondConfirmationResult, error) {
	return s.result, nil
}

func TestContinuationCallbackRunsAfterHTTPResponseIsWritten(t *testing.T) {
	response := httptest.NewRecorder()
	called := false
	handler := NewHandler(HandlerOptions{
		Control: handlerControlStub{result: control.RespondConfirmationResult{
			Accepted:     true,
			Continuation: &control.Continuation{Kind: "append", SessionID: "session"},
		}},
		Authenticate: func(ctx context.Context, _ *http.Request) (context.Context, bool, error) {
			return ctx, true, nil
		},
		Continue: func(_ context.Context, continuation control.Continuation) {
			called = true
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accepted":true`) {
				t.Errorf("continuation called before response was written: status=%d body=%q", response.Code, response.Body.String())
			}
			if continuation.SessionID != "session" {
				t.Errorf("continuation=%+v", continuation)
			}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/confirmation/respond", strings.NewReader(`{"decision":"approve","scope":"current"}`))
	handler.ServeHTTP(response, request)
	if !called {
		t.Fatal("continuation callback was not called")
	}
}
