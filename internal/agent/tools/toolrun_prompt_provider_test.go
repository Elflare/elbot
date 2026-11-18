package tools

import (
	"context"
	"reflect"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestPromptNamesReadToolAvailability(t *testing.T) {
	ctx := contextinfo.WithToolAvailability(context.Background(), contextinfo.ToolAvailability{Tools: []string{"read_file"}, Skills: []string{"nls"}})
	got, err := (ToolRunPromptProvider{}).ToolNames(ctx, storage.SessionModeWork, &storage.Session{}, session.Scope{})
	want := agentshare.PromptToolNames{Tools: []string{"read_file"}, Skills: []string{"nls"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, error = %v", got, err)
	}
}

func TestPromptNamesOmitUnavailableModes(t *testing.T) {
	provider := ToolRunPromptProvider{}
	for _, mode := range []string{storage.SessionModeChat, storage.SessionModeBackground} {
		got, err := provider.ToolNames(context.Background(), mode, &storage.Session{}, session.Scope{})
		if err != nil || len(got.Tools)+len(got.Skills) != 0 {
			t.Fatalf("mode %s: names = %#v, error = %v", mode, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.ToolNames(ctx, storage.SessionModeWork, nil, session.Scope{}); err == nil {
		t.Fatal("canceled context was accepted")
	}
}
