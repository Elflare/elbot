package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"slices"
	"testing"
	"time"
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

func TestFormatChatHistoryLineIncludesReferenceContext(t *testing.T) {
	row := toolshare.Message{
		Key: toolshare.MessageKey{Platform: "qqonebot", MessageID: "84"}, SenderID: "2002", SenderName: "回复者",
		Text:      "本次发送的内容",
		Reply:     toolshare.ReplyContext{MessageID: "42", SenderID: "1001", SenderName: "被引用者", Text: "被引用内容"},
		CreatedAt: time.Date(2026, 9, 12, 15, 4, 5, 0, time.Local),
	}
	want := `=> [#84] 2026-09-12 15:04:05 回复者(2002): [引用#42：被引用者(qq:1001):被引用内容]

本次发送的内容`
	if got := formatChatHistoryLine(row, "84", false, "...[单条消息过长，已截断]"); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}
