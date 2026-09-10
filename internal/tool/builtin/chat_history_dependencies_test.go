package builtin

import (
	"slices"
	"testing"

	"elbot/internal/tool"
)

func TestChatHistoryToolsMutuallyDiscoverable(t *testing.T) {
	tools := []tool.Tool{
		NewSearchChatHistoryTool(nil),
		NewGetChatHistoryAroundTool(nil),
		NewReplyToChatHistoryMessageTool(nil),
		NewGetMediaTool(nil, nil),
	}
	registry := tool.NewRegistry()
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
			details, errs := registry.DiscoverDetails([]string{value.Name()}, nil)
			if info.Hidden {
				if len(details) != 0 || len(errs) != 1 {
					t.Fatalf("hidden tool exposed: %#v, errors = %#v", details, errs)
				}
			} else if len(errs) != 0 || len(details) != len(tools) {
				t.Fatalf("discovery = %#v, errors = %#v", details, errs)
			}
			if info.Hidden != (value.Name() != "search_chat_history") {
				t.Fatalf("hidden setting changed: %#v", info)
			}
		})
	}
}
