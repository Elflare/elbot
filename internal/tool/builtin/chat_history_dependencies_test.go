package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"slices"
	"testing"
)

func withBuiltinToolAvailability(t *testing.T, ctx context.Context, registry *toolregistry.Registry) context.Context {
	t.Helper()
	availability, err := discovery.NewResolver(registry).Availability(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return contextinfo.WithToolAvailability(ctx, availability)
}

func TestChatHistoryToolsMutuallyDiscoverable(t *testing.T) {
	tools := []toolshare.ToolDefinition{
		(SearchChatHistoryTool{}).Definition(),
		(GetChatHistoryAroundTool{}).Definition(),
		(ReplyToChatHistoryMessageTool{}).Definition(),
		(GetMediaTool{}).Definition(),
	}
	registry := toolregistry.NewRegistry(security.NewPermission("low", "high", nil))
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, tools); err != nil {
		t.Fatal(err)
	}
	for _, value := range tools {
		t.Run(value.Definition.Name, func(t *testing.T) {
			info := value.Definition
			if !slices.Contains(info.Tags, "chat") || len(info.DependsOn) != len(tools)-1 {
				t.Fatalf("chat group/dependencies = %#v", info)
			}
			for _, other := range tools {
				if slices.Contains(info.DependsOn, other.Definition.Name) != (other.Definition.Name != value.Definition.Name) {
					t.Fatalf("unexpected dependency on %s: %#v", other.Definition.Name, info.DependsOn)
				}
			}
			ctx := withBuiltinToolAvailability(t, context.Background(), registry)
			details, err := discovery.NewResolver(registry).DiscoverDetails(ctx, []string{value.Definition.Name})
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
			if info.Hidden != (value.Definition.Name != "search_chat_history") {
				t.Fatalf("hidden setting changed: %#v", info)
			}
		})
	}
}
