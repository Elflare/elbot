package dispatch

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/media"
	"errors"
	"testing"
)

type usageMedia struct {
	*media.MediaCenter
}

func (m *usageMedia) Info(context.Context, string) (media.MediaInfo, error) {
	return media.MediaInfo{ID: "media:test"}, nil
}
func (m *usageMedia) ResolveForOutput(context.Context, string) (delivery.Source, media.UsageID, error) {
	return delivery.Source{Path: "prepared"}, "usage:one", nil
}

func TestSendPublishesUsageAfterSendWithoutChangingReceipt(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cancel"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sent, finished := 0, 0
			router := New(Options{Primary: testSender{}, Media: &usageMedia{}, PublishUsageFinished: func(_ context.Context, ids []media.UsageID) {
				if sent != 1 || len(ids) != 1 || ids[0] != "usage:one" {
					t.Fatalf("early or wrong finish: %d %v", sent, ids)
				}
				finished++
			}})
			receipt, result, err := router.sendPreparedMedia(ctx, []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{MediaID: "media:test"}}}, func(outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				sent++
				if finished != 0 || outputs[0].Source.Path != "prepared" {
					t.Fatal("finished during send")
				}
				if canceled {
					cancel()
					return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, events.LogResult{ResultStatus: events.ResultCanceled}, ctx.Err()
				}
				return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			})
			if finished != 1 || len(receipt.PlatformMessageIDs) != 1 || canceled && !errors.Is(err, context.Canceled) || !canceled && (err != nil || result.ResultStatus != events.ResultSucceeded) {
				t.Fatal(receipt, result, err, finished)
			}
		})
	}
}
