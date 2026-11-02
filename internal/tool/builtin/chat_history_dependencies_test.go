package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"slices"
	"testing"
)

func TestChatHistoryToolsMutuallyDiscoverable(t *testing.T) {
	tools := []toolshare.Tool{
		NewSearchChatHistoryTool(nil, nil),
		NewGetChatHistoryAroundTool(nil, nil),
		NewReplyToChatHistoryMessageTool(nil),
		NewGetMediaTool(nil),
	}
	registry := toolregistry.NewRegistry(security.NewPermission("low", "high", nil))
	for _, value := range tools {
		if err := registry.Register(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range tools {
		t.Run(value.Name(), func(t *testing.T) {
			info := value.Info()
			if !slices.Contains(info.Tags, "chat") || len(info.DependsOn) != len(tools)-1 {
				t.Fatalf("chat group/dependencies = %#v", info)
			}
			for _, other := range tools {
				if slices.Contains(info.DependsOn, other.Name()) != (other.Name() != value.Name()) {
					t.Fatalf("unexpected dependency on %s: %#v", other.Name(), info.DependsOn)
				}
			}
			details, err := discovery.NewResolver(registry).DiscoverDetails(context.Background(), []string{value.Name()})
			if err != nil {
				t.Fatal(err)
			}
			if info.Hidden {
				if len(details.Tools) != 0 || len(details.Errors) != 1 {
					t.Fatalf("hidden tool exposed: %#v", details)
				}
			} else if len(details.Errors) != 0 || len(details.Tools) != len(tools) {
				t.Fatalf("discovery = %#v", details)
			}
			if info.Hidden != (value.Name() != "search_chat_history") {
				t.Fatalf("hidden setting changed: %#v", info)
			}
		})
	}
}
