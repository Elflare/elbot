package remote

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/delivery"
	clishare "elbot/internal/platform/cli/share"
	"elbot/internal/platform/share"
	"github.com/coder/websocket/wsjson"
	"testing"
)

func TestCommandResultCorrelatesOnlyWithOriginConnection(t *testing.T) {
	server := testRemoteServer(t)
	if err := server.Start(t.Context(), func(ctx context.Context, text string, msg share.MessageContext) error {
		if msg.CommandReply == nil {
			t.Error("command reply not attached")
			return nil
		}
		_, err := msg.CommandReply.SendCommandResult(ctx, "status", &command.Result{Kind: command.ResultKindText, Text: text}, nil)
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
		if got.Type != remoteMsgCommandResult || got.RequestID != tc.id || got.Command != "status" || got.Result == nil || got.Result.Text != tc.id || got.Error != nil {
			t.Fatalf("reply=%+v", got)
		}
	}
}

func TestRemoteClientPresentsCommandResultAsIndependentReply(t *testing.T) {
	client := &RemoteClient{output: make(chan clishare.DisplayEvent, 1)}
	client.handleServerMessage(remoteMessage{Type: remoteMsgCommandResult, Result: &command.Result{Kind: command.ResultKindText, Text: "confirm?", Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{Label: "yes", Command: "/confirm"}}}}})
	event := <-client.output
	if event.Kind != clishare.DisplayChat || event.Text != "confirm?" {
		t.Fatalf("event=%+v", event)
	}
}
