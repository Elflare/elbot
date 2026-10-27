package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	agentoutput "elbot/internal/agent/output"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/dispatch"
	"elbot/internal/signal"
)

func TestReplyDeliveryRecordsOncePerSend(t *testing.T) {
	for _, mode := range []string{"direct", "buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplyTestFixture(t, mode == "buffered")
			f.ctx = contextinfo.WithExecution(f.ctx, contextinfo.Execution{SessionID: f.in.Session.ID, RequestID: "delivery-request"})
			f.platform.outputsReceipt = delivery.Receipt{
				PlatformMessageIDs: []string{"emoticon"},
				SentMessages:       []delivery.SentMessage{{Platform: "telegram", ScopeID: "private:actual", PlatformMessageID: "emoticon", OutputIndexes: []int{0}}},
			}
			if mode == "stream" {
				f.in.Stream = &replyTestStream{events: &f.events, wantCtx: f.requestCtx, t: t, replaceReceipt: f.platform.receipt}
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
				if r.EventName == "reply_delivered" {
					records = append(records, r)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			if _, err := f.out.SendOutputs(f.ctx, nil); err != nil || len(records) != 0 {
				t.Fatalf("empty outputs logged: err=%v records=%+v", err, records)
			}
			result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || !result.Persisted || len(records) != 2 {
				t.Fatalf("result=%+v err=%v records=%+v", result, err, records)
			}
			for i, r := range records {
				if r.EventName != "reply_delivered" || r.Level != slog.LevelDebug || r.ResultStatus != events.ResultSucceeded || r.Identity.SessionID == nil || *r.Identity.SessionID != f.in.Session.ID || r.Identity.RequestID == nil || *r.Identity.RequestID != "delivery-request" {
					t.Fatalf("delivery record=%+v", r)
				}
				want := []string{"platform_message_ids=[sent]", "platform_message_ids=[emoticon]"}[i]
				if !strings.Contains(r.Content, want) {
					t.Fatalf("missing %q: %s", want, r.Content)
				}
			}
		})
	}
}

func TestIntermediateStreamDeliveryLogsOnce(t *testing.T) {
	for _, tc := range []struct {
		name, text            string
		replaceErr, finishErr error
		status                events.ResultStatus
		level                 slog.Level
	}{
		{"text", "visible", nil, nil, events.ResultSucceeded, slog.LevelDebug},
		{"empty", "", nil, nil, events.ResultSucceeded, slog.LevelDebug},
		{"replace_failure", "visible", errors.New("replace failed"), nil, events.ResultFailed, slog.LevelError},
		{"finish_canceled", "visible", nil, context.Canceled, events.ResultCanceled, slog.LevelInfo},
		{"empty_finish_failure", "", nil, errors.New("finish failed"), events.ResultFailed, slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplyTestFixture(t, false)
			stream := &replyTestStream{events: &f.events, wantCtx: f.requestCtx, t: t,
				replaceReceipt: f.platform.receipt, finishReceipt: f.platform.receipt,
				replaceErr: tc.replaceErr, finishErr: tc.finishErr,
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
				if r.EventName == "reply_delivered" {
					records = append(records, r)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			err = f.out.FinishIntermediate(f.ctx, f.requestCtx, stream, tc.text, true)
			if (tc.replaceErr != nil && !errors.Is(err, tc.replaceErr)) || (tc.replaceErr == nil && !errors.Is(err, tc.finishErr)) {
				t.Fatalf("business error changed: %v", err)
			}
			if len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level || !strings.Contains(records[0].Content, "platform_message_ids=[sent]") {
				t.Fatalf("stream records=%+v", records)
			}
			wantCalls := "replace,finish"
			if tc.replaceErr != nil {
				wantCalls = "replace"
			} else if tc.text == "" {
				wantCalls = "finish"
			}
			if strings.Join(f.events, ",") != wantCalls {
				t.Fatalf("stream calls changed: %v", f.events)
			}
		})
	}
}

func TestBufferedSendFailureKeepsReceipt(t *testing.T) {
	for _, sendErr := range []error{errors.New("send failed"), context.Canceled} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			f := newReplyTestFixture(t, true)
			f.platform.sendErr = sendErr
			result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if !errors.Is(err, sendErr) || !result.Persisted || len(result.Receipt.SentMessages) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSendOutputsUsesSourceLogResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
		level  slog.Level
	}{
		{"success", events.ResultSucceeded, nil, slog.LevelDebug},
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
			ctx := context.Background()
			sender := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: dispatch.New(dispatch.Options{Primary: backend})})
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
				if record.EventName == "reply_delivered" && strings.HasPrefix(record.Content, "operation=send_outputs") {
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
					if record.EventName == "reply_delivered" {
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
				if records[0].ResultStatus != status || records[0].Level != tc.level || !strings.HasPrefix(content, "operation=send_assistant_message\n") || !strings.Contains(content, "platform_message_ids=[sent]") {
					t.Fatalf("reply facts changed: %+v", records[0])
				}
				if strings.Contains(content, "\nfinish:") {
					t.Fatalf("empty finish section: %s", content)
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
				if record.EventName == "reply_delivered" {
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
			if len(records) != 2 || records[0].ResultStatus != events.ResultFailed || records[0].Level != slog.LevelError || records[1].ResultStatus != events.ResultCanceled || records[1].Level != slog.LevelInfo {
				t.Fatalf("later cancellation hid an independent failure: %#v", records)
			}
			if !strings.Contains(records[1].Content, "operation=send_outputs\nlater outputs canceled") {
				t.Fatalf("outputs mislabeled: %s", records[1].Content)
			}
		})
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
