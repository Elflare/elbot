package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
)

func observerSignals() agentevents.Signals {
	return agentevents.Signals{
		UserInputReceived:   signal.New[agentevents.UserInputReceivedEvent]("input"),
		PersistenceFailed:   signal.New[agentevents.PersistenceFailedEvent]("persistence"),
		TurnTimedOut:        signal.New[agentevents.TurnTimedOutEvent]("timeout"),
		ModelCallCompleted:  signal.New[agentevents.ModelCallCompletedEvent]("model"),
		ToolCallCompleted:   signal.New[agentevents.ToolCallCompletedEvent]("tool"),
		ConfirmationChanged: signal.New[agentevents.ConfirmationChangedEvent]("confirmation"),
		ToolDenied:          signal.New[agentevents.ToolDeniedEvent]("denied"),
		StatusChanged:       signal.New[agentevents.StatusChangedEvent]("status"),
		VisionFallbackUsed:  signal.New[agentevents.VisionFallbackUsedEvent]("vision"),
		HookFailed:          signal.New[agentevents.HookFailedEvent]("hook"),
		ReplyDelivered:      signal.New[agentevents.ReplyDeliveredEvent]("delivery"),
		ReplyCommitted:      signal.New[agentevents.ReplyCommittedEvent]("commit"),
	}
}

type recordHandler struct {
	records chan slog.Record
	fail    bool
}

func (h recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.records <- r.Clone()
	if h.fail {
		return errors.New("disk write failed")
	}
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }
func recordAttrs(record slog.Record) map[string]any {
	attrs := map[string]any{}
	record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	return attrs
}
func awaitRecord(t *testing.T, records <-chan slog.Record) slog.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(time.Second):
		t.Fatal("missing log record")
		return slog.Record{}
	}
}

func TestProductionAssemblyObservesConversationExactlyOnce(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	records := make(chan slog.Record, 512)
	logger := slog.New(recordHandler{records: records})
	captureLogs(t, logger)
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Agent.HandleMessage(context.Background(), "observe this"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Signals.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for len(records) > 0 {
		counts[recordAttrs(<-records)["event"].(string)]++
	}
	for _, message := range []string{"user_message", "assistant_message", "reply_delivered", "reply_committed"} {
		if counts[message] != 1 {
			t.Fatalf("%s: %d records", message, counts[message])
		}
	}
}

func TestRunnerStartsQueueShutdownBeforeWaitingForProducer(t *testing.T) {
	for _, producer := range []string{"platform", "cron"} {
		t.Run(producer, func(t *testing.T) {
			var events []string
			runner := newTestRunner(t, &events, RunModeFull, "")
			runner.shutdownTimeout = time.Second
			q, err := signal.NewQueue(signal.QueueOptions{Capacity: 1, WaitForCapacity: true})
			if err != nil {
				t.Fatal(err)
			}
			b := &signalBindings{queues: []*signal.Queue{q}}
			ready, release, started, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			blockedProducer := func() error {
				defer close(exited)
				if err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
					return err
				}
				<-started
				if err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { return nil }}); err != nil {
					return err
				}
				close(ready)
				err := q.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { t.Error("unaccepted log ran"); return nil }})
				close(release)
				if !errors.Is(err, signal.ErrClosed) {
					return errors.New("blocked admission was not closed")
				}
				return nil
			}
			base := runner.deps.Foundation
			runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
				f, err := base.Build(ctx, req)
				if producer == "cron" {
					f.StopCron = func(ctx context.Context) error {
						select {
						case <-exited:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
				return f, err
			})
			runner.deps.Runtime = runtimeFactoryFunc(func(context.Context, RuntimeRequest) (*RuntimeComponents, error) {
				return &RuntimeComponents{Handler: handlerStub{}, Signals: b, Lifecycle: lifecycleFunc(func(context.Context) error { return nil })}, nil
			})
			runner.deps.Executor = executorFunc(func(context.Context, PlatformRunRequest) error {
				if producer == "platform" {
					return blockedProducer()
				}
				go func() {
					if err := blockedProducer(); err != nil {
						t.Error(err)
					}
				}()
				<-ready
				return nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- runner.Run(ctx, Options{}) }()
			<-ready
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown deadlocked on producer")
			}
			if !b.stopped() {
				t.Fatal("queue did not drain")
			}
			select {
			case <-exited:
			default:
				t.Fatal("producer never left admission")
			}
		})
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Connect(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(record.At, record.Level, record.Summary, 0)
		out.AddAttrs(record.Fields...)
		out.AddAttrs(slog.String("event", record.Name), slog.String("module", record.Module))
		if record.Detail != "" && logger.Enabled(ctx, slog.LevelDebug) {
			out.AddAttrs(slog.String("detail", record.Detail))
		}
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
