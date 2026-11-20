package transfer

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/media/share"
	"elbot/internal/operationresult"
)

func TestPrepareOutputsUsesHeldInfoAndAbortsWholeBatch(t *testing.T) {
	fault := errors.New("sign failed")
	first := &fakeHeld{info: share.MediaInfo{ID: "one", Name: "one.png", MIMEType: "image/png"}}
	second := &fakeHeld{info: share.MediaInfo{ID: "two"}, sign: func(context.Context) (string, error) { return "", fault }}
	objects := &fakeObjects{hold: func(_ context.Context, id string) (operationresult.Result[share.HeldMedia], error) {
		held := first
		if id == "two" {
			held = second
		}
		return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: held}, nil
	}} // 没有 Info 实现：已有 ID 必须从这次持有取元数据。
	m := NewManager(objects, Options{})
	req := OutputRequest{Index: 2, Source: delivery.Source{MediaID: "one"}}
	got, usage, err := m.PrepareOutputs(t.Context(), []OutputRequest{req})
	if err != nil || got.Status != operationresult.Success || len(got.Data) != 1 || usage == "" {
		t.Fatal(got, usage, err)
	}
	item := got.Data[0]
	if item.Index != 2 || item.Name != "one.png" || item.Source.MediaID != "one" || item.Source.MIMEType != "image/png" || first.closes.Load() != 0 {
		t.Fatal(item)
	}
	m.FinishUsages([]string{usage})
	if err := m.CleanupUsages(t.Context(), []string{usage}); err != nil {
		t.Fatal(err)
	}
	got, usage, err = m.PrepareOutputs(t.Context(), []OutputRequest{req, {Index: 3, Source: delivery.Source{MediaID: "two"}}, {Index: 4, Source: delivery.Source{URL: "not attempted"}}})
	if !errors.Is(err, fault) || usage != "" || got.Data != nil || first.closes.Load() != 2 || second.closes.Load() != 1 {
		t.Fatalf("got=%v usage=%q err=%v closes=%d,%d", got, usage, err, first.closes.Load(), second.closes.Load())
	}
}
