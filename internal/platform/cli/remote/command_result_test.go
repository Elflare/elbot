package remote

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	clishare "elbot/internal/platform/cli/share"
	"elbot/internal/platform/share"
	"encoding/json"
	"github.com/coder/websocket/wsjson"
	"reflect"
	"testing"
)

func TestCommandResultCorrelatesOnlyWithOriginConnection(t *testing.T) {
	server := testRemoteServer(t)
	if err := server.Start(t.Context(), func(ctx context.Context, text string, msg share.MessageContext) error {
		if msg.CommandReply == nil {
			t.Error("command reply not attached")
			return nil
		}
		request := command.Request{Raw: "/status", Prefix: "/", Name: "status"}
		_, err := msg.CommandReply.SendCommandResult(ctx, request, command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: text}}, nil)
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
		wantData, _ := json.Marshal(command.TextData{Content: tc.id})
		if got.Type != remoteMsgCommandResult || got.RequestID != tc.id || got.Command != (command.Request{Raw: "/status", Prefix: "/", Name: "status"}) || got.Result == nil || !reflect.DeepEqual(got.Result.Data, json.RawMessage(wantData)) || got.Error != nil {
			t.Fatalf("reply=%+v", got)
		}
	}
}

func TestRemoteClientPresentsCommandResultAsIndependentReply(t *testing.T) {
	client := &RemoteClient{output: make(chan clishare.DisplayEvent, 1)}
	client.handleServerMessage(remoteMessage{Type: remoteMsgCommandResult, Result: &command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "confirm?"}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{Label: "yes", Command: "/confirm"}}}}})
	event := <-client.output
	if event.Kind != clishare.DisplayChat || event.Text != "confirm?" {
		t.Fatalf("event=%+v", event)
	}
}
