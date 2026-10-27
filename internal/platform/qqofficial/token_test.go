package qqofficial

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/delivery"
)

type tokenRoundTripper func(*http.Request) (*http.Response, error)

func (f tokenRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func tokenHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestTokenWaitCancellationDoesNotCancelRefresh(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "ordinary"
		if force {
			name = "forced"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			client := &http.Client{Transport: tokenRoundTripper(func(r *http.Request) (*http.Response, error) {
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				close(entered)
				select {
				case <-release:
					return tokenHTTPResponse(http.StatusOK, `{"access_token":"fresh","expires_in":3600}`), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})}
			source := newTokenSource(Config{AppID: "app", ClientSecret: "secret"}, client)
			owner := make(chan error, 1)
			go func() { _, err := source.tokenValue(t.Context()); owner <- err }()
			<-entered
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			waiter := make(chan error, 1)
			go func() {
				var err error
				if force {
					_, err = source.forceRefresh(ctx)
				} else {
					_, err = source.tokenValue(ctx)
				}
				waiter <- err
			}()
			returned := false
			select {
			case err := <-waiter:
				returned = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled waiter = %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("canceled waiter is blocked behind the refresh request")
			}
			select {
			case err := <-owner:
				t.Fatalf("waiting cancellation ended owner: %v", err)
			default:
			}
			close(release)
			if err := <-owner; err != nil {
				t.Error(err)
			}
			if !returned {
				<-waiter
			}
		})
	}
}

func TestCanceledTokenCallDoesNotTouchCache(t *testing.T) {
	var requests atomic.Int32
	source := newTokenSource(Config{}, &http.Client{Transport: tokenRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})})
	source.token = "cached"
	source.expiresAt = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, call := range []func(context.Context) (string, error){source.tokenValue, source.forceRefresh} {
		if _, err := call(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled call = %v", err)
		}
	}
	if source.token != "cached" || requests.Load() != 0 {
		t.Fatalf("cache=%q requests=%d", source.token, requests.Load())
	}
}

func TestConcurrentTokenRequestsShareRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var requests atomic.Int32
		source := newTokenSource(Config{}, &http.Client{Transport: tokenRoundTripper(func(*http.Request) (*http.Response, error) {
			if requests.Add(1) == 1 {
				close(entered)
			}
			<-release
			return tokenHTTPResponse(http.StatusOK, `{"access_token":"shared","expires_in":3600}`), nil
		})})
		var calls sync.WaitGroup
		for range 16 {
			calls.Go(func() {
				token, err := source.tokenValue(t.Context())
				if err != nil || token != "shared" {
					t.Errorf("token=%q error=%v", token, err)
				}
			})
		}
		<-entered
		synctest.Wait()
		close(release)
		calls.Wait()
		if requests.Load() != 1 {
			t.Fatalf("refresh requests=%d", requests.Load())
		}
	})
}

func TestCanceledRefreshReleasesNextWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		var requests atomic.Int32
		source := newTokenSource(Config{}, &http.Client{Transport: tokenRoundTripper(func(r *http.Request) (*http.Response, error) {
			if requests.Add(1) == 1 {
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return tokenHTTPResponse(http.StatusOK, `{"access_token":"retry","expires_in":3600}`), nil
		})})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first, next := make(chan error, 1), make(chan error, 1)
		go func() { _, err := source.tokenValue(ctx); first <- err }()
		<-entered
		go func() {
			token, err := source.tokenValue(t.Context())
			if err == nil && token != "retry" {
				err = errors.New("wrong retry token")
			}
			next <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("owner=%v", err)
		}
		if err := <-next; err != nil {
			t.Fatal(err)
		}
		if requests.Load() != 2 {
			t.Fatalf("requests=%d", requests.Load())
		}
	})
}

func TestFailedForcedRefreshDoesNotReuseRejectedToken(t *testing.T) {
	requests := 0
	source := newTokenSource(Config{}, &http.Client{Transport: tokenRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return tokenHTTPResponse(http.StatusServiceUnavailable, `unavailable`), nil
		}
		return tokenHTTPResponse(http.StatusOK, `{"access_token":"new","expires_in":3600}`), nil
	})})
	source.token = "rejected"
	source.expiresAt = time.Now().Add(time.Hour)
	if _, err := source.forceRefresh(t.Context()); err == nil {
		t.Fatal("expected refresh failure")
	}
	token, err := source.tokenValue(t.Context())
	if err != nil || token != "new" || requests != 2 {
		t.Fatalf("token=%q requests=%d error=%v", token, requests, err)
	}
}

func TestUnauthorizedRefreshRetriesOnlyOnce(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		name := "success"
		if rejected {
			name = "rejected_again"
		}
		t.Run(name, func(t *testing.T) {
			cfg := Config{AppID: "app", ClientSecret: "secret"}
			applyDefaults(&cfg)
			client := newAPIClient(cfg)
			defer client.http.CloseIdleConnections()
			requests, refreshes := 0, 0
			transport := tokenRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == defaultTokenURL {
					refreshes++
					return tokenHTTPResponse(http.StatusOK, `{"access_token":"new","expires_in":3600}`), nil
				}
				requests++
				expected := "QQBot new"
				if requests == 1 {
					expected = "QQBot old"
				}
				if got := r.Header.Get("Authorization"); got != expected {
					t.Errorf("authorization=%q", got)
				}
				if requests == 1 || rejected {
					return tokenHTTPResponse(http.StatusUnauthorized, `{"message":"rejected"}`), nil
				}
				return tokenHTTPResponse(http.StatusOK, `{"id":"sent"}`), nil
			})
			client.http.Transport = transport
			client.tokens.token = "old"
			client.tokens.expiresAt = time.Now().Add(time.Hour)
			var result messageResponse
			err := client.doJSON(t.Context(), http.MethodPost, "/v2/users/u/messages", messageToCreate{Content: "hello"}, &result)
			if (err != nil) != rejected || requests != 2 || refreshes != 1 {
				t.Fatalf("requests=%d refreshes=%d error=%v", requests, refreshes, err)
			}
			if !rejected && result.ID != "sent" {
				t.Fatalf("response=%+v", result)
			}
		})
	}
}

func TestIngressStopsWhileOutgoingTokenRefreshContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		adapter, err := NewFromPlatformConfig(map[string]any{"enabled": true, "app_id": "app", "client_secret": "secret"}, Deps{})
		if err != nil {
			t.Fatal(err)
		}
		transport := tokenRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() == defaultTokenURL {
				close(entered)
				select {
				case <-release:
					return tokenHTTPResponse(http.StatusOK, `{"access_token":"fresh","expires_in":3600}`), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			if r.URL.Path == "/gateway" {
				t.Error("canceled gateway must not request HTTP")
				return nil, context.Canceled
			}
			return tokenHTTPResponse(http.StatusOK, `{"id":"sent"}`), nil
		})
		adapter.client.http.Transport = transport
		sendDone := make(chan error, 1)
		go func() {
			_, _, err := adapter.SendTo(t.Context(), delivery.Target{PrivateUserID: "u"}, []delivery.Output{delivery.Text("finish")})
			sendDone <- err
		}()
		<-entered
		if err := adapter.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		// 确保接入协程已阻塞在令牌等待，而不是尚未调度。
		synctest.Wait()
		adapter.BeginClose()
		synctest.Wait()
		select {
		case <-adapter.done:
		default:
			t.Error("ingress still waits for the outgoing refresh")
		}
		select {
		case err := <-sendDone:
			t.Fatalf("ingress stop ended sending: %v", err)
		default:
		}
		close(release)
		if err := <-sendDone; err != nil {
			t.Fatal(err)
		}
		if err := adapter.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := adapter.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
