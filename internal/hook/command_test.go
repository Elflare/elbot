package hook

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"elbot/internal/command"
	"elbot/internal/hook/control"
	"elbot/internal/hook/share"
)

type commandTestControl struct {
	control.HookService
	list func() []share.Info
	stop func(context.Context, string) (bool, error)
}

func (c commandTestControl) HookList() []share.Info             { return c.list() }
func (c commandTestControl) StatefulHooks() []share.RuntimeInfo { return nil }
func (c commandTestControl) StopHook(ctx context.Context, id string) (bool, error) {
	return c.stop(ctx, id)
}

func TestCommandsPreserveDrainAndJoinAdmittedCalls(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "Handle", true: "Complete"}[complete], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, runtime := newBridgeTestSupervisor(t)
				entered, release := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				service := commandTestControl{list: func() []share.Info { close(entered); <-release; return nil }}
				m.commands = []command.Handler{control.NewHooks(service, m.beginCall, testCommandText)}
				handlers := m.Commands()
				handler := handlers[0]
				handlers[0] = nil
				if m.Commands()[0] == nil {
					t.Fatal("Commands exposed its backing slice")
				}
				m.BeginClose()
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					if complete {
						handler.(command.Completer).Complete(context.Background(), command.CompletionRequest{Raw: "/hooks ", Name: "hooks", Prefix: "/", Cursor: 7})
					} else {
						_, _ = handler.Handle(context.Background(), command.Request{})
					}
				}()
				<-entered
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := m.Close(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("Close=%v", err)
				}
				synctest.Wait()
				select {
				case <-m.Done():
					t.Fatal("Done closed before command returned")
				case <-runtime.Done():
					t.Fatal("runtime closed before command returned")
				default:
				}
				if _, err := handler.Handle(context.Background(), command.Request{}); !errors.Is(err, context.Canceled) {
					t.Fatalf("closed handler=%v", err)
				}
				if got := handler.(command.Completer).Complete(context.Background(), command.CompletionRequest{}); len(got) != 0 {
					t.Fatalf("closed completion=%v", got)
				}
				close(release)
				<-finished
				if err := m.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestCommandStopReceivesCloseCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newBridgeTestSupervisor(t)
		entered := make(chan context.Context, 1)
		service := commandTestControl{stop: func(ctx context.Context, _ string) (bool, error) {
			entered <- ctx
			<-ctx.Done()
			return false, ctx.Err()
		}}
		handler := control.NewHooks(service, m.beginCall, testCommandText)
		finished := make(chan *command.Result, 1)
		go func() {
			result, err := handler.Handle(context.Background(), command.Request{Args: "stop worker"})
			if err != nil {
				t.Error(err)
			}
			finished <- result
		}()
		ctx := <-entered
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("command context was not canceled")
		}
		if result := <-finished; result == nil || !strings.Contains(result.Content, "context canceled") {
			t.Fatalf("command result=%v", result)
		}
	})
}

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }
