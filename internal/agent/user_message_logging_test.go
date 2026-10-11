package agent

import (
	"context"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
)

func TestUserMessageLogSourceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		conversation contextinfo.Conversation
		want         string
	}{
		{"platform", contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ScopeID: "group:456"}, Identity: contextinfo.Identity{PlatformUserID: "123"}, PlatformMessageID: "789"}, "platform=qqonebot\nplatform_user_id=123\nscope_id=group:456\nplatform_message_id=789\ntext=hello"},
		{"partial", contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}}, "platform=cli\ntext=hello"},
		{"absent", contextinfo.Conversation{}, "text=hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
			ctx, row, err := a.execution.ResolveInput(t.Context(), "logging")
			if err != nil {
				t.Fatal(err)
			}
			ctx = contextinfo.WithConversation(ctx, tc.conversation)
			ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{RequestID: "request"})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "user_message" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			if _, err := a.dialogue.PrepareTurn(ctx, dialogue.TurnInput{Session: row, Text: "hello", Selection: modelmgr.SelectionForTurn(ctx, a.execution.models, row)}); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 {
				t.Fatalf("records=%+v", records)
			}
			r := records[0]
			if r.Content != tc.want || r.Identity.SessionID == nil || *r.Identity.SessionID != row.ID || r.Identity.RequestID == nil || *r.Identity.RequestID != "request" {
				t.Fatalf("record=%+v, want content=%q", r, tc.want)
			}
		})
	}
}
