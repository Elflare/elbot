package qqonebot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/delivery"
)

func TestForwardTextSendingPaths(t *testing.T) {
	for _, target := range []target{{MessageType: "private", UserID: 1}, {MessageType: "group", GroupID: 9}} {
		for _, mode := range []string{"chat", "context notice", "explicit notice", "temporary notice", "temporary context"} {
			for _, length := range []int{3000, 3001, 6000, 6001} {
				t.Run(fmt.Sprintf("%s/%s/%d", target.MessageType, mode, length), func(t *testing.T) {
					requests := make(chan request, 4)
					transport := newTestTransport(t, func(req request) response {
						requests <- req
						return response{Data: []byte(`{"message_id":88,"forward_id":"resource-not-a-message-id"}`)}
					})
					a := New(Config{URL: transport.URL}, nil, nil)
					a.transport = transport
					runes := []rune(strings.Repeat("娅🔥", 3001))
					body := string(runes[:length])
					outputs := []delivery.Output{delivery.Text(string(runes[:100])), delivery.Text(string(runes[100:length]))}
					ctx := testTargetContext(target)
					var receipt delivery.Receipt
					var err error
					switch mode {
					case "chat":
						receipt, err = a.SendChat(ctx, outputs)
					case "context notice":
						receipt, err = a.SendNotice(ctx, delivery.Notice{Outputs: outputs})
					case "explicit notice":
						// The explicit target must win over a different source conversation.
						ctx = testTargetContext(qqTargetForTest("private", 777))
						receipt, err = a.SendNotice(ctx, delivery.Notice{Target: delivery.Target{ScopeID: oneBotTargetScope(target)}, Outputs: outputs})
					case "temporary notice":
						receipt, err = a.SendNotice(delivery.WithTemporaryConnection(context.Background()), delivery.Notice{Target: delivery.Target{ScopeID: oneBotTargetScope(target)}, Outputs: outputs})
					case "temporary context":
						receipt, err = a.SendNotice(delivery.WithTemporaryConnection(ctx), delivery.Notice{Outputs: outputs})
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(requests) != 1 {
						t.Fatalf("send requests = %d, want 1", len(requests))
					}
					req := <-requests
					wantAction := "send_" + target.MessageType + "_msg"
					if length > qqTextPageRunes {
						wantAction = "send_" + target.MessageType + "_forward_msg"
					}
					if req.Action != wantAction {
						t.Fatalf("action = %q, want %q", req.Action, wantAction)
					}
					idKey, targetID := "user_id", target.UserID
					if target.MessageType == "group" {
						idKey, targetID = "group_id", target.GroupID
					}
					if req.Params[idKey] != float64(targetID) {
						t.Fatalf("target = %#v", req.Params[idKey])
					}
					if length <= qqTextPageRunes {
						if req.Params["message"] != body || req.Params["auto_escape"] != true {
							t.Fatal("short text changed or is not escaped")
						}
					} else {
						var nodes []struct {
							Type string `json:"type"`
							Data struct {
								Content []Segment `json:"content"`
							} `json:"data"`
						}
						encoded, err := json.Marshal(req.Params["messages"])
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(encoded, &nodes); err != nil {
							t.Fatal(err)
						}
						if len(nodes) != (length+2999)/3000 {
							t.Fatalf("nodes = %d", len(nodes))
						}
						var joined strings.Builder
						for _, node := range nodes {
							if node.Type != "node" || len(node.Data.Content) != 1 || node.Data.Content[0].Type != "text" {
								t.Fatalf("invalid node: %#v", node)
							}
							page := segmentDataString(node.Data.Content[0].Data, "text")
							if len([]rune(page)) > qqTextPageRunes {
								t.Fatal("node exceeds text limit")
							}
							joined.WriteString(page)
						}
						if joined.String() != body {
							t.Fatal("forward changed original text")
						}
					}
					if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "88" || len(receipt.SentMessages) != 1 {
						t.Fatalf("receipt = %#v", receipt)
					}
					if sent := receipt.SentMessages[0]; sent.PlatformMessageID != "88" || sent.Platform != "qqonebot" || sent.ScopeID != oneBotTargetScope(target) || len(sent.OutputIndexes) != 0 {
						t.Fatalf("sent message = %#v", sent)
					}
				})
			}
		}
	}
}

func qqTargetForTest(kind string, id int64) target {
	if kind == "group" {
		return target{MessageType: kind, GroupID: id}
	}
	return target{MessageType: kind, UserID: id}
}

func TestForwardSendFailureDoesNotFallBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp response
	}{
		{name: "API failure", resp: response{Status: "failed", Retcode: 100}},
		{name: "missing message id", resp: response{Data: []byte(`{"forward_id":"resource"}`)}},
		{name: "invalid message id", resp: response{Data: []byte(`{"message_id":"invalid"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			transport := newTestTransport(t, func(req request) response {
				calls.Add(1)
				return tc.resp
			})
			a := New(Config{URL: transport.URL}, nil, nil)
			a.transport = transport
			receipt, err := a.SendChat(testTargetContext(target{MessageType: "group", GroupID: 9}), []delivery.Output{delivery.Text(strings.Repeat("a", 3001))})
			if err == nil || calls.Load() != 1 || len(receipt.PlatformMessageIDs) != 0 || len(receipt.SentMessages) != 0 {
				t.Fatalf("receipt=%#v error=%v calls=%d", receipt, err, calls.Load())
			}
		})
	}
}

func TestForwardAdminNoticesKeepPartialReceipts(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		t.Run(fmt.Sprint(temporary), func(t *testing.T) {
			var calls atomic.Int32
			transport := newTestTransport(t, func(req request) response {
				calls.Add(1)
				if req.Action != "send_private_forward_msg" {
					t.Errorf("action=%s", req.Action)
				}
				if req.Params["user_id"] == float64(2) {
					return response{Status: "failed", Retcode: 1}
				}
				return response{Data: []byte(`{"message_id":88}`)}
			})
			a := New(Config{URL: transport.URL, Superadmins: []string{"1", "2", "3"}}, nil, nil)
			a.transport = transport
			ctx := context.Background()
			if temporary {
				ctx = delivery.WithTemporaryConnection(ctx)
			}
			receipt, err := a.SendNotice(ctx, delivery.Notice{Target: delivery.Target{Superadmins: true}, Outputs: []delivery.Output{delivery.Text(strings.Repeat("a", 3001))}})
			if err == nil || calls.Load() != 2 || len(receipt.PlatformMessageIDs) != 1 || len(receipt.SentMessages) != 1 || receipt.SentMessages[0].ScopeID != "private:1" {
				t.Fatalf("receipt=%#v error=%v calls=%d", receipt, err, calls.Load())
			}
		})
	}
}

func TestLongMixedOutputKeepsNormalMessageAPI(t *testing.T) {
	requests := make(chan request, 1)
	transport := newTestTransport(t, func(req request) response {
		requests <- req
		return response{Data: []byte(`{"message_id":88}`)}
	})
	a := New(Config{URL: transport.URL}, nil, nil)
	a.transport = transport
	_, err := a.SendChat(testTargetContext(target{MessageType: "group", GroupID: 9}), []delivery.Output{delivery.Text(strings.Repeat("a", 3001)), delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/image.png"}}})
	if err != nil {
		t.Fatal(err)
	}
	if req := <-requests; req.Action != "send_group_msg" {
		t.Fatalf("action = %s", req.Action)
	}
}
