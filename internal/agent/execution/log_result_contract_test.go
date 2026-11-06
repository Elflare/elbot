package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	agentoutput "elbot/internal/agent/output"

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

func (f outputSendFunc) StartStream(context.Context) (delivery.MessageStream, error) {
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type != delivery.StreamSend {
			return delivery.Receipt{}, nil
		}
		return f([]delivery.Output{{Kind: delivery.KindText, Text: event.Text, Actions: event.Actions}})
	}), nil
}

func TestAssistantSendFailureReturnsWithoutLowerReport(t *testing.T) {
	f := newReplyTestFixture(t, false)
	want := errors.New("send failed")
	f.platform.sendErr = want
	counts := map[string]int{}
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
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

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	receipt, err := f.out.Send(f.ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "message"})
	if !errors.Is(err, want) || len(receipt.PlatformMessageIDs) != 1 || counts["delivery"] != 0 || counts["agent"] != 1 {
		t.Fatal("receipt= result= err= logs=", receipt, err, counts)
	}
}

func TestBufferedSendFailureKeepsReceipt(t *testing.T) {
	for _, sendErr := range []error{errors.New("send failed"), context.Canceled} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			f := newReplyTestFixture(t, true)
			f.platform.sendErr = sendErr
			result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || result.MessageID == "" || len(result.Receipt.SentMessages) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSendOutputsRecordsReceiptsAndReturnsFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
		level  slog.Level
	}{
		{"success", events.ResultSucceeded, nil, slog.LevelDebug},
		{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("transport: %w", context.Canceled), slog.LevelInfo},
		{"mixed_failure", events.ResultFailed, errors.New("independent send failure"), slog.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
				if record.EventName == "reply_delivered" && readDeliveryLog(t, record).Operation == "send_outputs" {
					records = append(records, record)
				}

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
			_, err = sender.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: []delivery.Output{delivery.Text("business text")}})
			if err != tc.err {
				t.Fatal("error:", err)
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultSucceeded || records[0].Level != slog.LevelDebug {
				t.Fatalf("records=%#v", records)
			}
			if content := readDeliveryLog(t, records[0]); content.Operation != "send_outputs" || content.Detail != "" {
				t.Fatalf("source diagnostic changed: %s", records[0].Content)
			}
		})
	}
}

func TestReplyCommitReportsSendFailureSeparatelyFromReceipts(t *testing.T) {
	for _, mode := range []string{"direct", "buffered"} {
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
				var records []events.LogRecord
				failures := make(chan events.LogRecord, 1)
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
					if record.EventName == "reply_delivered" {
						records = append(records, record)
					}
					if record.EventName == "reply_send_failed" {
						failures <- record
					}

				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(connection.Disconnect)
				result, err := fixture.committer.Commit(fixture.ctx, fixture.requestCtx, fixture.in, fixture.out)
				if err != nil || len(result.Receipt.PlatformMessageIDs) != 1 || len(records) != 1 {
					t.Fatalf("result=%+v err=%v records=%+v", result, err, records)
				}
				content := readDeliveryLog(t, records[0])
				if records[0].ResultStatus != events.ResultSucceeded || records[0].Level != slog.LevelDebug || content.Operation != "send_assistant_message" || !slices.Contains(content.PlatformMessageIDs, "sent") || content.Detail != "" {
					t.Fatalf("reply facts changed: %+v", records[0])
				}
				if sendErr != nil {
					select {
					case record := <-failures:
						if record.Content != diagnostic || record.ResultStatus != status || record.Level != tc.level {
							t.Fatalf("source diagnostic changed: %+v", record)
						}
					case <-time.After(time.Second):
						t.Fatal("send failure was not reported")
					}
				}
			})
		}
	}
}
