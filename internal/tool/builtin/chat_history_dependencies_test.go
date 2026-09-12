package builtin

import (
	"slices"
	"testing"
	"time"

	"elbot/internal/storage"
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

func TestFormatChatHistoryLineIncludesReferenceContext(t *testing.T) {
	row := storage.ChatMessage{
		Platform: "qqonebot", PlatformMessageID: "84", SenderID: "2002", SenderName: "回复者",
		Text: "本次发送的内容", ReplyToPlatformMessageID: "42",
		Metadata:  `{"reply":{"sender_id":"1001","sender_name":"被引用者","text":"被引用内容"}}`,
		CreatedAt: time.Date(2026, 9, 12, 15, 4, 5, 0, time.Local),
	}
	want := `=> [#84] 2026-09-12 15:04:05 回复者(2002): [引用#42：被引用者(qq:1001):被引用内容]

本次发送的内容`
	if got := formatChatHistoryLine(row, "84"); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}
