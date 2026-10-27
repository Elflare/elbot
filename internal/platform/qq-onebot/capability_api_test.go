// capability_api_test.go 验证通用能力映射、参数校验及原生透传边界。
package qqonebot

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/testutil"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestCapabilityMappingAndNativePassthrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requests := make(chan request, 8)
		server := testutil.NewVirtualDefaultHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			for {
				var req request
				if wsjson.Read(r.Context(), conn, &req) != nil {
					return
				}
				requests <- req
				resp := response{Status: "ok", Echo: req.Echo, Data: []byte(`{"native":true}`)}
				if req.Action == "fail" {
					resp.Status = "failed"
					resp.Retcode = 100
				}
				if wsjson.Write(r.Context(), conn, resp) != nil {
					return
				}
			}
		}))
		transport, ctx := connectTestTransport(t, server, time.Second)
		a := &Adapter{transport: transport}
		tests := []struct {
			name, action string
			params, want map[string]any
			raw          bool
		}{
			{"message.recall", "delete_msg", map[string]any{"message_id": "-12"}, map[string]any{"message_id": float64(-12)}, false},
			{"member.mute", "set_group_ban", map[string]any{"user_id": json.Number("3"), "group_id": int64(4), "duration_seconds": float64(60)}, map[string]any{"user_id": float64(3), "group_id": float64(4), "duration": float64(60)}, false},
			{"chat.leave", "set_group_leave", map[string]any{"group_id": 4, "is_dismiss": true}, map[string]any{"group_id": float64(4), "is_dismiss": true}, false},
			{"message.recall", "message.recall", map[string]any{"custom": "untouched"}, map[string]any{"custom": "untouched"}, true},
		}
		for _, tt := range tests {
			before, _ := json.Marshal(tt.params)
			call := a.CallCapability
			if tt.raw {
				call = a.CallPlatformAPI
			}
			data, err := call(ctx, tt.name, tt.params)
			if err != nil || string(data) != `{"native":true}` {
				t.Fatalf("%s = %s/%v", tt.name, data, err)
			}
			req := <-requests
			if req.Action != tt.action || !reflect.DeepEqual(req.Params, tt.want) {
				t.Fatalf("request = %#v; want %s/%#v", req, tt.action, tt.want)
			}
			after, _ := json.Marshal(tt.params)
			if string(before) != string(after) {
				t.Fatal("caller params mutated")
			}
		}
		for _, tt := range []struct {
			name   string
			params map[string]any
		}{
			{"unknown", nil}, {"message.recall", nil}, {"message.recall", map[string]any{"message_id": 1.5}},
			{"member.mute", map[string]any{"user_id": 1, "duration_seconds": 0, "group_id": 2}},
			{"member.mute", map[string]any{"user_id": 1, "duration_seconds": 3}}, {"chat.leave", nil},
		} {
			if _, err := a.CallCapability(ctx, tt.name, tt.params); err == nil {
				t.Fatalf("invalid capability %s succeeded", tt.name)
			}
		}
		select {
		case req := <-requests:
			t.Fatalf("invalid capability sent request: %#v", req)
		default:
		}
		if _, err := a.CallPlatformAPI(context.Background(), "fail", nil); err == nil {
			t.Fatal("native failure was hidden")
		}
	})
}
