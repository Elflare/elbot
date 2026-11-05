package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/httpclient"
	"elbot/internal/llm/responses"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type modelBody struct {
	io.Reader
	closed bool
}

func (b *modelBody) Close() error { b.closed = true; return nil }

type failedModelReader struct{ err error }

func (r failedModelReader) Read([]byte) (int, error) { return 0, r.err }

func TestModelListRetriesReadFailures(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, failure := range []string{"disconnect", "timeout", "syntax"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				calls, notices := 0, 0
				var lastBody *modelBody
				ctx := t.Context()
				cause := error(io.ErrUnexpectedEOF)
				if failure == "timeout" {
					cause = context.DeadlineExceeded
				}
				transport := modelTransport(func(req *http.Request) (*http.Response, error) {
					if lastBody != nil && !lastBody.closed {
						t.Fatal("previous body still open")
					}
					calls++
					var reader io.Reader = strings.NewReader(`{"data":[{"id":"m"}]}`)
					if calls == 1 {
						reader = failedModelReader{cause}
						if failure == "syntax" {
							reader = strings.NewReader(`{"data":!}`)
						}
					}
					lastBody = &modelBody{Reader: reader}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: lastBody, Request: req}, nil
				})
				retry := llm.RetryOptions{MaxRetries: 2, RetryInitialDelay: time.Nanosecond, OnRetry: func(got context.Context, e llm.RetryEvent) {
					notices++
					if got != ctx || !errors.Is(e.Err, cause) || !lastBody.closed {
						t.Fatalf("retry lost cause/context or body: %+v", e)
					}
				}}
				httpOpts := httpclient.Options{HTTPClient: &http.Client{Transport: transport}}
				var client llm.Client
				var err error
				if protocol == "chat" {
					client, err = chatcompletions.New("http://models.invalid", "key", nil, nil, chatcompletions.RequestOptions{Options: httpOpts, RetryOptions: retry})
				} else {
					client, err = responses.New("http://models.invalid", "key", nil, nil, responses.RequestOptions{Options: httpOpts, RetryOptions: retry})
				}
				if err != nil {
					t.Fatal(err)
				}
				models, err := client.ListModels(ctx)
				if failure == "syntax" {
					var syntax *json.SyntaxError
					var mapped *llm.CallError
					if calls != 1 || notices != 0 || !errors.As(err, &syntax) || !errors.As(err, &mapped) || mapped.Retryable {
						t.Fatalf("calls=%d error=%v", calls, err)
					}
				} else if err != nil || calls != 2 || notices != 1 || len(models) != 1 || models[0] != "m" {
					t.Fatalf("models=%v calls=%d notices=%d error=%v", models, calls, notices, err)
				}
				if !lastBody.closed {
					t.Fatal("final body still open")
				}
			})
		}
	}
}
