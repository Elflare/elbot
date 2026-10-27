// capability_api_test.go 验证原生协议透传、参数不变与失败传播。
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

func TestNativeAPIPassthrough(t *testing.T) {
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
		}{
			{"custom_api", "custom_api", map[string]any{"custom": "untouched"}, map[string]any{"custom": "untouched"}},
		}
		for _, tt := range tests {
			before, _ := json.Marshal(tt.params)
			data, err := a.CallPlatformAPI(ctx, tt.name, tt.params)
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
		if _, err := a.CallPlatformAPI(context.Background(), "fail", nil); err == nil {
			t.Fatal("native failure was hidden")
		}
	})
}
