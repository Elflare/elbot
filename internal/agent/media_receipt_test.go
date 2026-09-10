package agent

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"testing"
	"time"
)

func TestMediaReceiptDoesNotGuessFallbackOrRetryCacheFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	a := &Agent{store: store, media: media.NewManager(store, root, &media.LocalBackend{Root: root}), mediaRetentionDays: 7}
	outputs := []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("image")}}}
	_, err := a.sendPreparedMedia(ctx, outputs, func([]delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"fallback"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := store.Media().FindOutputs(ctx, "telegram", "group:9", "fallback", time.Now())
	if err != nil || len(cached) != 0 {
		t.Fatalf("fallback cache = %#v / %v", cached, err)
	}
	calls := 0
	receipt, err := a.sendPreparedMedia(ctx, outputs, func([]delivery.Output) (delivery.Receipt, error) {
		calls++
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "group:9", PlatformMessageID: "sent", OutputIndexes: []int{0}}}}, nil
	})
	if err != nil || calls != 1 || len(receipt.PlatformMessageIDs) != 1 {
		t.Fatalf("cache failure changed send result: %#v %d %v", receipt, calls, err)
	}
}
