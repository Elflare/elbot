package agent

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/platform"
)

func TestPartialAssistantSendKeepsMessageAssociation(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			store := newTestStore(t)
			a := New(&fakePlatform{}, &fakeLLM{replies: []string{"long answer"}}, "test-model", config.ProviderConfig{}, store)
			wantErr := errors.New("later page failed")
			sender := mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"first-page"}}, wantErr
			})
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "test", ScopeID: "group:old"}, Identity: chatinfo.Identity{PlatformUserID: "user"}}, Sender: sender, BufferAssistantOutput: buffered})
			if err := a.HandleMessage(ctx, "question"); !errors.Is(err, wantErr) {
				t.Fatalf("send error=%v", err)
			}
			got, err := store.Messages().FindByPlatformMessage(ctx, "test", "group:old", "first-page")
			if err != nil || got.Content != "long answer" {
				t.Fatalf("successful page mapping=%#v/%v", got, err)
			}
		})
	}
}
