package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/signal"
)

func TestSendOutputsUsesSourceLogResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
		level  slog.Level
	}{
		{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("transport: %w", context.Canceled), slog.LevelInfo},
		{"partial_fanout_failure_without_business_error", events.ResultFailed, nil, slog.LevelError},
		{"mixed_failure", events.ResultFailed, errors.Join(context.Canceled, errors.New("independent send failure")), slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "reply_delivered" && strings.HasPrefix(record.Content, "operation=send_outputs") {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			businessReceipt := delivery.Receipt{
				PlatformMessageIDs: []string{"sent"},
				SentMessages:       []delivery.SentMessage{{Platform: "test", ScopeID: "group:1", PlatformMessageID: "sent", OutputIndexes: []int{0}}},
			}
			backend := mediaSendFunc(func([]delivery.Output) (delivery.Receipt, events.LogResult, error) {
				return businessReceipt, events.LogResult{ResultStatus: tc.status, Content: "source-specific diagnostic"}, tc.err
			})
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Sender: backend})
			sender := &OutputSender{dispatcher: dispatch.New(dispatch.Options{})}
			logResult, err := sender.SendOutputs(ctx, []delivery.Output{delivery.Text("business text")})
			if err != tc.err || logResult.ResultStatus != tc.status || !strings.Contains(logResult.Content, "source-specific diagnostic") {
				t.Fatalf("log result=%#v error=%v", logResult, err)
			}
			if len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level {
				t.Fatalf("records=%#v", records)
			}
			if !strings.Contains(records[0].Content, "platform_message_id=sent") || !strings.Contains(records[0].Content, "output_indexes=[0]") {
				t.Fatalf("partial-delivery facts missing: %s", records[0].Content)
			}
		})
	}
}

func TestReplyCommitUsesSourceOutcomeWithoutChangingBusiness(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			fixture := newReplyTestFixture(t, buffered)
			fixture.platform.outputsLogResult = &events.LogResult{ResultStatus: events.ResultFailed, Content: "one destination failed; another received the output"}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "reply_committed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			result, err := fixture.committer.Commit(fixture.ctx, fixture.requestCtx, fixture.in, fixture.out)
			if err != nil || !result.Persisted || len(result.Receipt.PlatformMessageIDs) != 1 {
				t.Fatalf("business result=%#v error=%v", result, err)
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultFailed || records[0].Level != slog.LevelError || !strings.Contains(records[0].Content, "one destination failed") {
				t.Fatalf("records=%#v", records)
			}
		})
	}
}

func TestReplyCommitLogSpacingComesFromSources(t *testing.T) {
	for _, mode := range []string{"direct", "buffered", "stream"} {
		for _, tc := range []struct {
			status     events.ResultStatus
			level      slog.Level
			diagnostic string
		}{
			{events.ResultSucceeded, slog.LevelDebug, ""},
			{events.ResultFailed, slog.LevelError, "first paragraph\n\nsecond paragraph"},
			{events.ResultCanceled, slog.LevelInfo, "send canceled"},
		} {
			t.Run(mode+"/"+tc.status.String(), func(t *testing.T) {
				fixture := newReplyTestFixture(t, mode == "buffered")
				fixture.in.Outputs = nil
				status, diagnostic := tc.status, tc.diagnostic
				source := &events.LogResult{ResultStatus: status, Content: diagnostic}
				fixture.platform.sendLogResult = source
				if mode == "stream" {
					fixture.in.Stream = &replyTestStream{
						events: &fixture.events, wantCtx: fixture.requestCtx, t: t,
						replaceReceipt: fixture.platform.receipt, replaceLogResult: source,
					}
				}
				var records []events.LogRecord
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
					if record.EventName == "reply_committed" {
						records = append(records, record)
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(connection.Disconnect)
				result, err := fixture.committer.Commit(fixture.ctx, fixture.requestCtx, fixture.in, fixture.out)
				if err != nil || !result.Persisted || len(result.Receipt.PlatformMessageIDs) != 1 || len(records) != 1 {
					t.Fatalf("result=%+v err=%v records=%+v", result, err, records)
				}
				content := records[0].Content
				if records[0].ResultStatus != status || records[0].Level != tc.level || !strings.Contains(content, "persisted=true\npreparation:") || !strings.Contains(content, "platform_message_ids=[sent]") {
					t.Fatalf("reply facts changed: %+v", records[0])
				}
				for _, unwanted := range []string{"\nassociation:", "\nfinish:", "\noutputs_send:", "assistant_send:\n\n"} {
					if strings.Contains(content, unwanted) {
						t.Fatalf("empty section or extra newline %q: %s", unwanted, content)
					}
				}
				if diagnostic == "" {
					if strings.Contains(content, "\n\n") || strings.HasSuffix(content, "\n") || strings.Contains(content, "\nreplace:") {
						t.Fatalf("source introduced blank lines or empty sections: %s", content)
					}
				} else if !strings.Contains(content, diagnostic) {
					t.Fatalf("diagnostic paragraph break was changed: %s", content)
				}
			})
		}
	}
}

func TestReplyCommitKeepsFailureBeforeLaterCancellation(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			fixture := newReplyTestFixture(t, buffered)
			fixture.platform.sendLogResult = &events.LogResult{ResultStatus: events.ResultFailed, Content: "one assistant destination failed"}
			fixture.platform.outputsLogResult = &events.LogResult{ResultStatus: events.ResultCanceled, Content: "later outputs canceled"}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "reply_committed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			result, err := fixture.committer.Commit(fixture.ctx, fixture.requestCtx, fixture.in, fixture.out)
			if err != nil || !result.Persisted || len(result.Receipt.PlatformMessageIDs) != 1 {
				t.Fatalf("business result=%#v error=%v", result, err)
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultFailed || records[0].Level != slog.LevelError {
				t.Fatalf("later cancellation hid an independent failure: %#v", records)
			}
			if strings.Contains(records[0].Content, "association:\nlater outputs canceled") {
				t.Fatalf("outputs mislabeled as association: %s", records[0].Content)
			}
		})
	}
}

func TestAssociateReceiptDeclaresWrappedCancellation(t *testing.T) {
	fixture := newReplyTestFixture(t, false)
	fixture.repo.mapErr = fmt.Errorf("database: %w", context.Canceled)
	failures, logResult := fixture.committer.AssociateReceipt(fixture.ctx, fixture.in.Session.ID, "local-message", fixture.platform.receipt)
	if len(failures) != 1 || !errors.Is(failures[0], context.Canceled) || logResult.ResultStatus != events.ResultCanceled {
		t.Fatalf("failures=%v log result=%#v", failures, logResult)
	}
	if !strings.HasPrefix(logResult.Content, "platform=") || strings.Contains(logResult.Content, "\n\n") {
		t.Fatalf("association source introduced blank lines: %q", logResult.Content)
	}
}

func TestStreamKeepsSourceFailureBeforeFinishCancellation(t *testing.T) {
	fixture := newReplyTestFixture(t, false)
	stream := &replyTestStream{
		events: &fixture.events, wantCtx: fixture.requestCtx, t: t,
		replaceReceipt:   delivery.Receipt{PlatformMessageIDs: []string{"partial"}},
		finishReceipt:    delivery.Receipt{PlatformMessageIDs: []string{"final"}},
		replaceLogResult: &events.LogResult{ResultStatus: events.ResultFailed, Content: "replace destination failed"},
		finishLogResult:  &events.LogResult{ResultStatus: events.ResultCanceled, Content: "finish canceled"},
	}
	receipt, logResult, err := fixture.out.ReplaceAndFinishStream(fixture.ctx, fixture.requestCtx, stream, "visible")
	if err != nil || len(receipt.PlatformMessageIDs) != 2 || logResult.ResultStatus != events.ResultFailed {
		t.Fatalf("receipt=%#v log result=%#v error=%v", receipt, logResult, err)
	}
	if !strings.Contains(logResult.Content, "replace destination failed") || !strings.Contains(logResult.Content, "finish canceled") {
		t.Fatalf("source body missing: %s", logResult.Content)
	}
}
