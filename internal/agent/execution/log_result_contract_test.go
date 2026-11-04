package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	agentoutput "elbot/internal/agent/output"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

type deliveryLogContent struct {
	Operation          string            `json:"operation"`
	Detail             string            `json:"detail"`
	PlatformMessageIDs []string          `json:"platform_message_ids"`
	SentMessages       []json.RawMessage `json:"sent_messages"`
}

func readDeliveryLog(t *testing.T, record events.LogRecord) deliveryLogContent {
	t.Helper()
	var content deliveryLogContent
	if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
		t.Fatal(err)
	}
	return content
}

type outputSendFunc func([]delivery.Output) (delivery.Receipt, error)

func (f outputSendFunc) SendTo(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	return f(outputs)
}

func (outputSendFunc) StartStream(context.Context) (delivery.MessageStream, error) {
	return nil, nil
}

func TestReplyDeliveryRecordsOncePerSend(t *testing.T) {
	for _, mode := range []string{"direct", "buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplyTestFixture(t, mode == "buffered")
			f.ctx = contextinfo.WithExecution(f.ctx, contextinfo.Execution{SessionID: f.in.Session.ID, RequestID: "delivery-request"})
			f.platform.outputsReceipt = delivery.Receipt{
				PlatformMessageIDs: []string{"emoticon"},
				SentMessages:       []delivery.SentMessage{{Platform: "telegram", ChatType: "private", ChatID: "actual", PlatformMessageID: "emoticon", OutputIndexes: []int{0}}},
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
			if err := f.out.SendOutputs(f.ctx, nil); err != nil || len(records) != 0 {
				t.Fatalf("empty outputs logged: err=%v records=%+v", err, records)
			}
			result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || result.MessageID == "" || len(records) != 2 {
				t.Fatalf("result=%+v err=%v records=%+v", result, err, records)
			}
			for i, r := range records {
				if r.EventName != "reply_delivered" || r.Level != slog.LevelDebug || r.ResultStatus != events.ResultSucceeded || r.Identity.SessionID == nil || *r.Identity.SessionID != f.in.Session.ID || r.Identity.RequestID == nil || *r.Identity.RequestID != "delivery-request" {
					t.Fatalf("delivery record=%+v", r)
				}
				want := []string{"sent", "emoticon"}[i]
				if !slices.Contains(readDeliveryLog(t, r).PlatformMessageIDs, want) {
					t.Fatalf("missing %q: %s", want, r.Content)
				}
			}
		})
	}
}

func TestAssistantSendFailureUsesUnifiedDeliveryLog(t *testing.T) {
	f := newReplyTestFixture(t, false)
	want := errors.New("send failed")
	f.platform.sendErr = want
	counts := map[string]int{}
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Module == "delivery" {
			counts["delivery"]++
			if record.ResultStatus != events.ResultFailed {
				t.Errorf("failure status=%v", record.ResultStatus)
			}
		}
		if record.Module == "agent" {
			counts["agent"]++
			if record.EventName != "reply_delivered" {
				t.Errorf("duplicate failure log: %s", record.EventName)
			}
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	receipt, err := f.out.SendAssistant(f.ctx, "message")
	if !errors.Is(err, want) || len(receipt.PlatformMessageIDs) != 1 || counts["delivery"] != 1 || counts["agent"] != 1 {
		t.Fatal("receipt= result= err= logs=", receipt, err, counts)
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
		{"replace_failure", "visible", errors.New("replace failed"), nil, events.ResultFailed, slog.LevelWarn},
		{"finish_canceled", "visible", nil, context.Canceled, events.ResultCanceled, slog.LevelInfo},
		{"empty_finish_failure", "", nil, errors.New("finish failed"), events.ResultFailed, slog.LevelWarn},
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
			if len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level || !slices.Contains(readDeliveryLog(t, records[0]).PlatformMessageIDs, "sent") {
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
			if !errors.Is(err, sendErr) || result.MessageID == "" || len(result.Receipt.SentMessages) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSendOutputsLogsBusinessFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
		level  slog.Level
	}{
		{"success", events.ResultSucceeded, nil, slog.LevelDebug},
		{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("transport: %w", context.Canceled), slog.LevelInfo},
		{"mixed_failure", events.ResultFailed, errors.Join(context.Canceled, errors.New("independent send failure")), slog.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "reply_delivered" && readDeliveryLog(t, record).Operation == "send_outputs" {
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
				SentMessages:       []delivery.SentMessage{{Platform: "test", ChatType: "group", ChatID: "1", PlatformMessageID: "sent", OutputIndexes: []int{0}}},
			}
			backend := outputSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
				return businessReceipt, tc.err
			})
			ctx := context.Background()
			sender := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Dispatcher: backend})
			err = sender.SendOutputs(ctx, []delivery.Output{delivery.Text("business text")})
			if err != tc.err {
				t.Fatal("error:", err)
			}
			if len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level {
				t.Fatalf("records=%#v", records)
			}
			if content := readDeliveryLog(t, records[0]); content.Operation != "send_outputs" || (tc.err == nil && content.Detail != "" || tc.err != nil && content.Detail != tc.err.Error()) {
				t.Fatalf("source diagnostic changed: %s", records[0].Content)
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
			{events.ResultFailed, slog.LevelWarn, "first paragraph\n\nsecond paragraph"},
			{events.ResultCanceled, slog.LevelInfo, "send canceled"},
		} {
			t.Run(mode+"/"+tc.status.String(), func(t *testing.T) {
				fixture := newReplyTestFixture(t, mode == "buffered")
				fixture.in.Outputs = nil
				status, diagnostic := tc.status, tc.diagnostic
				var sendErr error
				if status == events.ResultFailed {
					sendErr = errors.New(diagnostic)
				}
				if status == events.ResultCanceled {
					sendErr = context.Canceled
					diagnostic = sendErr.Error()
				}
				fixture.platform.sendErr = sendErr
				if mode == "stream" {
					fixture.in.Stream = &replyTestStream{
						events: &fixture.events, wantCtx: fixture.requestCtx, t: t,
						replaceReceipt: fixture.platform.receipt, replaceErr: sendErr,
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
				if !errors.Is(err, sendErr) || len(result.Receipt.PlatformMessageIDs) != 1 || len(records) != 1 {
					t.Fatalf("result=%+v err=%v records=%+v", result, err, records)
				}
				content := readDeliveryLog(t, records[0])
				if records[0].ResultStatus != status || records[0].Level != tc.level || content.Operation != "send_assistant_message" || !slices.Contains(content.PlatformMessageIDs, "sent") {
					t.Fatalf("reply facts changed: %+v", records[0])
				}
				if strings.Contains(content.Detail, "\nfinish:") {
					t.Fatalf("empty finish section: %+v", content)
				}
				if diagnostic == "" {
					if strings.Contains(content.Detail, "\n\n") || strings.HasSuffix(content.Detail, "\n") || strings.Contains(content.Detail, "\nreplace:") {
						t.Fatalf("source introduced blank lines or empty sections: %+v", content)
					}
				} else if !strings.Contains(content.Detail, diagnostic) {
					t.Fatalf("diagnostic paragraph break was changed: %+v", content)
				}
			})
		}
	}
}

func TestStreamPreservesReplaceReceiptWhenFinishCanceled(t *testing.T) {
	fixture := newReplyTestFixture(t, false)
	stream := &replyTestStream{
		events: &fixture.events, wantCtx: fixture.requestCtx, t: t,
		replaceReceipt: delivery.Receipt{PlatformMessageIDs: []string{"partial"}},
		finishReceipt:  delivery.Receipt{PlatformMessageIDs: []string{"final"}},

		finishErr: context.Canceled,
	}
	receipt, err := fixture.out.ReplaceAndFinishStream(fixture.ctx, fixture.requestCtx, stream, "visible")
	if !errors.Is(err, context.Canceled) || len(receipt.PlatformMessageIDs) != 2 {
		t.Fatal("receipt and error:", receipt, err)
	}

}
