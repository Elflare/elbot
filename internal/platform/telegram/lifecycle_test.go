// lifecycle_test.go 验证接入与完整操作分阶段关闭、取消隔离和实际退出。
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func awaitTelegram[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Telegram operation")
		var zero T
		return zero
	}
}

func telegramTestContext() context.Context {
	return contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ScopeID: "private:9"}})
}

func TestOperationAdmissionResult(t *testing.T) {
	for _, state := range []string{"canceled", "deadline", "closed"} {
		t.Run(state, func(t *testing.T) {
			a := newTestAdapter(t, Config{}, nil)
			s := newTestStream(t, a, "private:9")
			ctx := telegramTestContext()
			wantErr, wantStatus := error(context.Canceled), events.ResultCanceled
			switch state {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				wantErr, wantStatus = context.DeadlineExceeded, events.ResultFailed
			case "closed":
				if err := a.Close(ctx); err != nil {
					t.Fatal(err)
				}
				wantErr, wantStatus = signal.ErrClosed, events.ResultFailed
			}
			operations := map[string]func() (delivery.Receipt, events.LogResult, error){
				"SendChat": func() (delivery.Receipt, events.LogResult, error) {
					return a.SendChat(ctx, []delivery.Output{delivery.Text("hello")})
				},
				"SendTo": func() (delivery.Receipt, events.LogResult, error) {
					return a.SendTo(ctx, delivery.Target{PrivateUserID: "9"}, []delivery.Output{delivery.Text("hello")})
				},
				"Replace": func() (delivery.Receipt, events.LogResult, error) { return s.Replace(ctx, "hello") },
				"Finish":  func() (delivery.Receipt, events.LogResult, error) { return s.Finish(ctx) },
			}
			for name, operation := range operations {
				_, result, err := operation()
				if !errors.Is(err, wantErr) || result.ResultStatus != wantStatus {
					t.Errorf("%s: error=%v status=%v, want %v/%v", name, err, result.ResultStatus, wantErr, wantStatus)
				}
			}
		})
	}
}

func TestStreamQueuedCancellationResult(t *testing.T) {
	for _, operation := range []string{"Replace", "Finish"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := newTestAdapter(t, Config{}, nil)
				s := newTestStream(t, a, "private:9").(*messageStream)
				s.gate <- struct{}{}
				s.text = "original"
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				go func() {
					defer close(done)
					var result events.LogResult
					var err error
					if operation == "Replace" {
						_, result, err = s.Replace(ctx, "replacement")
					} else {
						_, result, err = s.Finish(ctx)
					}
					if !errors.Is(err, context.Canceled) || result.ResultStatus != events.ResultCanceled {
						t.Errorf("error=%v status=%v, want canceled", err, result.ResultStatus)
					}
				}()
				synctest.Wait()
				cancel()
				<-done
				if len(s.gate) != 1 || s.text != "original" || s.finished {
					t.Fatal("canceled waiter changed the owner or stream state")
				}
				<-s.gate
			})
		})
	}
}

func TestRuntimeStopsIngressAndWaitsForAcceptedWork(t *testing.T) {
	polling := make(chan struct{})
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"username":"bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if polls.Add(1) == 1 {
				fmt.Fprint(w, `{"ok":true,"result":[{"update_id":7,"message":{"message_id":8,"chat":{"id":9,"type":"private"},"text":"hello"}}]}`)
				return
			}
			close(polling)
			<-r.Context().Done()
		default:
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
		}
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL}, nil)
	entered, canceled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var handled atomic.Int32
	if err := a.Start(context.Background(), func(ctx context.Context, text string, msg share.MessageContext) error {
		handled.Add(1)
		if text != "hello" || msg.Bot.UserID != "123" {
			t.Errorf("text=%q bot=%#v", text, msg.Bot)
		}
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	awaitTelegram(t, entered)
	awaitTelegram(t, polling)
	observer, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Wait(observer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait=%v", err)
	}
	select {
	case <-canceled:
		t.Fatal("canceling observation stopped work")
	default:
	}
	a.BeginClose()
	awaitTelegram(t, canceled)
	a.acceptUpdate(context.Background(), func(context.Context, string, share.MessageContext) error { handled.Add(1); return nil }, update{Message: &message{Text: "late"}}, share.Identity{})
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while handler alive=%v", err)
	}
	if _, _, err := a.SendChat(telegramTestContext(), []delivery.Output{delivery.Text("cleanup")}); err != nil {
		t.Fatalf("cleanup send=%v", err)
	}
	once.Do(func() { close(release) })
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handled.Load() != 1 {
		t.Fatalf("handled=%d", handled.Load())
	}
	if err := a.chatHistory.Append(context.Background(), &storage.ChatMessage{Platform: platformName, PlatformScopeID: "private:9", PlatformMessageID: "after-close", Text: "still open"}); err != nil {
		t.Fatalf("borrowed history closed: %v", err)
	}
	if err := a.Wait(observer); err != nil {
		t.Fatalf("completed Wait=%v", err)
	}
}

func TestStartDoesNotWaitForRemoteAndCloseCancelsPolling(t *testing.T) {
	for _, blocked := range []string{"getMe", "getUpdates"} {
		t.Run(blocked, func(t *testing.T) {
			entered, canceled := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if strings.HasSuffix(r.URL.Path, "/"+blocked) {
					close(entered)
					<-r.Context().Done()
					close(canceled)
					return
				}
				fmt.Fprint(w, `{"ok":true,"result":{"id":123}}`)
			}))
			t.Cleanup(server.Close)
			a := newTestAdapter(t, Config{APIBaseURL: server.URL}, nil)
			started := make(chan error, 1)
			go func() { started <- a.Start(context.Background(), nil) }()
			if err := awaitTelegram(t, started); err != nil {
				t.Fatal(err)
			}
			awaitTelegram(t, entered)
			if err := a.Start(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			a.BeginClose()
			if err := a.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			awaitTelegram(t, canceled)
		})
	}
}

func TestOperationsRemainAvailableUntilFinalClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			fmt.Fprint(w, `{"ok":true,"result":{"file_path":"test","file_size":3}}`)
		case strings.HasSuffix(r.URL.Path, "/test"):
			fmt.Fprint(w, "abc")
		default:
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
		}
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL, FileBaseURL: server.URL}, nil)
	idle := newTestStream(t, a, "private:9")
	a.BeginClose()
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := telegramTestContext()
	operations := map[string]func() error{
		"SendChat": func() error { _, _, err := a.SendChat(ctx, []delivery.Output{delivery.Text("hello")}); return err },
		"SendTo": func() error {
			_, _, err := a.SendTo(ctx, delivery.Target{PrivateUserID: "9"}, []delivery.Output{delivery.Text("hello")})
			return err
		},
		"API": func() error { _, err := a.CallPlatformAPI(ctx, "getChat", nil); return err },
		"media": func() error {
			_, err := a.ResolveMedia(ctx, share.MessageSegment{PlatformFileID: "file"}, 10)
			return err
		},
		"StartStream": func() error { _, err := a.StartStream(ctx); return err },
		"Append":      func() error { return idle.Append(ctx, "hello") },
		"Replace":     func() error { _, _, err := idle.Replace(ctx, "final"); return err },
		"Finish":      func() error { _, _, err := idle.Finish(ctx); return err },
	}
	// Stream methods use a deterministic order; other operations have no ordering dependency.
	for _, name := range []string{"SendChat", "SendTo", "API", "media", "StartStream", "Append", "Replace", "Finish"} {
		if err := operations[name](); err != nil {
			t.Fatalf("%s after BeginClose: %v", name, err)
		}
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, signal.ErrClosed) {
			t.Errorf("%s after Close: %v", name, err)
		}
	}
}

type telegramRoundTripper func(*http.Request) (*http.Response, error)

func (f telegramRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloseCancelsAndWaitsForWholeOperation(t *testing.T) {
	a := newTestAdapter(t, Config{}, nil)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	a.client.http.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release // Simulate a transport still unwinding after cancellation.
		return nil, r.Context().Err()
	})
	result := make(chan error, 1)
	go func() { _, err := a.CallPlatformAPI(context.Background(), "getMe", nil); result <- err }()
	awaitTelegram(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close=%v", err)
	}
	awaitTelegram(t, canceled)
	select {
	case <-a.closeDone:
		t.Fatal("announced cleanup before actual exit")
	default:
	}
	once.Do(func() { close(release) })
	if err := awaitTelegram(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("operation=%v", err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStreamWaitCancellationDoesNotCancelOwner(t *testing.T) {
	a := newTestAdapter(t, Config{}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	a.client.http.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":77}}`)), Header: make(http.Header)}, nil
	})
	s := newTestStream(t, a, "private:9")
	owner := make(chan error, 1)
	go func() { owner <- s.Append(context.Background(), "hello") }()
	awaitTelegram(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	waiter := make(chan error, 1)
	go func() { _, _, err := s.Replace(ctx, "canceled replacement"); waiter <- err }()
	if err := awaitTelegram(t, waiter); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter=%v", err)
	}
	select {
	case err := <-owner:
		t.Fatalf("owner interrupted: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := awaitTelegram(t, owner); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, _, err := s.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentLifecycleAndCanceledStart(t *testing.T) {
	for _, mode := range []string{"unstarted", "canceled", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestAdapter(t, Config{}, nil)
			a.client.http.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := a.Start(ctx, nil); !errors.Is(err, context.Canceled) {
					t.Fatalf("Start=%v", err)
				}
			}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if mode == "concurrent" {
						if err := a.Start(context.Background(), nil); err != nil && !errors.Is(err, signal.ErrClosed) {
							t.Error(err)
						}
					}
					a.BeginClose()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := a.Close(ctx); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if err := a.Start(context.Background(), nil); !errors.Is(err, signal.ErrClosed) {
				t.Fatalf("restart=%v", err)
			}
		})
	}
}

func TestFinalCloseCancelsEveryHTTPBackedOperation(t *testing.T) {
	for _, name := range []string{"SendChat", "SendTo", "media", "Append", "Replace"} {
		t.Run(name, func(t *testing.T) {
			a := newTestAdapter(t, Config{}, nil)
			s := newTestStream(t, a, "private:9")
			entered := make(chan struct{})
			a.client.http.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			result := make(chan error, 1)
			go func() {
				ctx := telegramTestContext()
				var err error
				switch name {
				case "SendChat":
					_, _, err = a.SendChat(ctx, []delivery.Output{delivery.Text("hello")})
				case "SendTo":
					_, _, err = a.SendTo(ctx, delivery.Target{PrivateUserID: "9"}, []delivery.Output{delivery.Text("hello")})
				case "media":
					_, err = a.ResolveMedia(ctx, share.MessageSegment{PlatformFileID: "file"}, 10)
				case "Append":
					err = s.Append(ctx, "hello")
				case "Replace":
					_, _, err = s.Replace(ctx, "hello")
				}
				result <- err
			}()
			awaitTelegram(t, entered)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := awaitTelegram(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation=%v", err)
			}
		})
	}
}

func TestFinalCloseReleasesOwnedIdleConnection(t *testing.T) {
	idle, closed := make(chan struct{}, 1), make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			idle <- struct{}{}
		case http.StateClosed:
			closed <- struct{}{}
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL}, nil)
	if _, _, err := a.SendChat(telegramTestContext(), []delivery.Output{delivery.Text("hello")}); err != nil {
		t.Fatal(err)
	}
	awaitTelegram(t, idle)
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitTelegram(t, closed)
}
