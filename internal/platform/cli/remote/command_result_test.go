package remote

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	clishare "elbot/internal/platform/cli/share"
	"elbot/internal/platform/share"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"strings"
	"testing"
)

func TestCommandResultCorrelatesOnlyWithOriginConnection(t *testing.T) {
	server := testRemoteServer(t)
	if err := server.Start(t.Context(), func(ctx context.Context, text string, msg share.MessageContext) error {
		if msg.CommandReply == nil {
			t.Error("command reply not attached")
			return nil
		}
		_, err := msg.CommandReply.SendCommandResult(ctx, command.TextResult(operationresult.Success, text), nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first, second := dialTestRemote(t, server), dialTestRemote(t, server)
	for _, tc := range []struct {
		id     string
		second bool
	}{{"one", false}, {"two", true}, {"three", false}} {
		conn := first
		if tc.second {
			conn = second
		}
		if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, ID: tc.id, Text: tc.id}); err != nil {
			t.Fatal(err)
		}
		got := readTestRemote(t, conn)
		if got.Type != remoteMsgCommandResult || got.RequestID != tc.id || got.Result == nil || delivery.ContentBlocksText(got.Result.Blocks) != tc.id || got.Error != nil {
			t.Fatalf("reply=%+v", got)
		}
	}
}

func TestRemoteClientPresentsCommandResultAsNotice(t *testing.T) {
	client := &RemoteClient{output: make(chan clishare.DisplayEvent, 1)}
	client.handleServerMessage(remoteMessage{Type: remoteMsgCommandResult, Result: &command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Blocks: []delivery.ContentBlock{{Kind: delivery.ContentText, Content: "confirm?"}}, Actions: []delivery.Action{{Label: "yes", Command: "/confirm"}}}})
	event := <-client.output
	if event.Kind != clishare.DisplayNotice || event.Text != "confirm?\n\n/confirm yes" {
		t.Fatalf("event=%+v", event)
	}
}

func TestLongCommandResultOverWebSocket(t *testing.T) {
	body := strings.Repeat("+很长的变更\n", 10000)
	server := testRemoteServer(t)
	if err := server.Start(t.Context(), func(ctx context.Context, text string, m share.MessageContext) error {
		r := command.TextResult(operationresult.Success, body)
		r.Actions = []delivery.Action{{ID: "confirm", Command: "-c", Label: "确认"}}
		_, err := m.CommandReply.SendCommandResult(ctx, r, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{cliChunksProtocol, ""} {
		t.Run("protocol="+proto, func(t *testing.T) {
			var c *websocket.Conn
			if proto != "" {
				c = dialTestRemote(t, server, proto)
			} else {
				c = dialTestRemote(t, server)
				c.SetReadLimit(int64(len(body) * 2))
			}
			if c.Subprotocol() != proto {
				t.Fatal(c.Subprotocol())
			}
			for _, id := range []string{"one", "two"} {
				if err := wsjson.Write(t.Context(), c, remoteMessage{Type: remoteMsgInput, ID: id, Text: "/detail"}); err != nil {
					t.Fatal(err)
				}
				var m remoteMessage
				var err error
				if proto == "" {
					err = wsjson.Read(t.Context(), c, &m)
				} else {
					err = readServerMessage(t.Context(), c, &m)
				}
				if err != nil {
					t.Fatal(err)
				}
				if m.Type != remoteMsgCommandResult || m.RequestID != id || m.Result == nil || delivery.ContentBlocksText(m.Result.Blocks) != body || len(m.Result.Actions) != 1 {
					t.Fatal("wrong or incomplete response", m.Type, m.RequestID)
				}
			}
		})
	}
}
