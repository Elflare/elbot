package dispatch

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"errors"
	"testing"
)

type usageMedia struct {
	*media.MediaCenter
}

func (*usageMedia) PrepareOutputs(context.Context, []media.OutputRequest) (operationresult.Result[[]media.PreparedOutput], media.UsageID, error) {
	return operationresult.Result[[]media.PreparedOutput]{Status: operationresult.Success, Data: []media.PreparedOutput{{Source: delivery.Source{Path: "prepared"}}}}, "usage:one", nil
}

func TestSendPublishesUsageAndDiscardsFailedReceipt(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cancel"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sent, finished := 0, 0
			sender := testSender{send: func(ctx context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
				sent++
				if finished != 0 || outputs[0].Source.Path != "prepared" {
					t.Fatal("finished during send")
				}
				if canceled {
					cancel()
				}
				return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, ctx.Err()
			}}
			router := New(Options{Primary: sender, Media: &usageMedia{}, PublishUsageFinished: func(_ context.Context, ids []media.UsageID) {
				if sent != 1 || len(ids) != 1 || ids[0] != "usage:one" {
					t.Fatalf("early or wrong finish: %d %v", sent, ids)
				}
				finished++
			}})
			receipt, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{MediaID: "media:test"}}})
			if finished != 1 || canceled && (!errors.Is(err, context.Canceled) || receipt.Status != "" || len(receipt.Data.PlatformMessageIDs) != 0) || !canceled && (err != nil || receipt.Status != operationresult.Success || len(receipt.Data.PlatformMessageIDs) != 1) {
				t.Fatal(receipt, err, finished)
			}
		})
	}
}
