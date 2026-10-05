package agent

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/media"
	"elbot/internal/platform"
	"testing"
	"time"
)

func TestMediaReceiptDoesNotGuessFallbackOrRetryCacheFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	router := dispatch.New(dispatch.Options{Store: store, Media: media.NewManager(store, root, &media.LocalBackend{Root: root}), MediaRetentionDays: 7})
	outputs := []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("image")}}}
	sender := mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"fallback"}}, nil
	})
	_, err := router.SendChat(platform.WithMessageContext(ctx, platform.MessageContext{Sender: sender}), outputs)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := store.Media().FindOutputs(ctx, "telegram", "group:9", "fallback", time.Now())
	if err != nil || len(cached) != 0 {
		t.Fatalf("fallback cache = %#v / %v", cached, err)
	}
	calls := 0
	sender = mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
		calls++
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "group:9", PlatformMessageID: "sent", OutputIndexes: []int{0}}}}, nil
	})
	receipt, err := router.SendChat(platform.WithMessageContext(ctx, platform.MessageContext{Sender: sender}), outputs)
	if err != nil || calls != 1 || len(receipt.PlatformMessageIDs) != 1 {
		t.Fatalf("cache failure changed send result: %#v %d %v", receipt, calls, err)
	}
}

type mediaSendFunc func([]delivery.Output) (delivery.Receipt, error)

func (f mediaSendFunc) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return f(outputs)
}
func (f mediaSendFunc) SendNotice(_ context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return f(notice.Outputs)
}
