// capability_api_test.go 验证通用能力映射及独立原生入口。
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCapabilityMappingsAndIndependentNativeAPI(t *testing.T) {
	type request struct {
		method string
		params map[string]any
	}
	requests := make(chan request, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		requests <- request{strings.TrimPrefix(r.URL.Path, "/bottest-token/"), params}
		fmt.Fprint(w, `{"ok":true,"result":{"accepted":true}}`)
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL}, nil)
	for _, tc := range []struct {
		name, method string
		params       map[string]any
	}{
		{"message.recall", "deleteMessage", map[string]any{"chat_id": "-9", "message_id": json.Number("17")}},
		{"member.mute", "restrictChatMember", map[string]any{"chat_id": int64(-9), "user_id": float64(5), "duration_seconds": 60}},
		{"chat.leave", "leaveChat", map[string]any{"chat_id": -9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.params)
			start := time.Now().Unix()
			result, err := a.CallCapability(context.Background(), tc.name, tc.params)
			if err != nil || string(result) != `{"accepted":true}` {
				t.Fatalf("result=%s err=%v", result, err)
			}
			got := awaitTelegram(t, requests)
			if got.method != tc.method || got.params["chat_id"] != float64(-9) {
				t.Fatalf("request=%#v", got)
			}
			switch tc.name {
			case "message.recall":
				if got.params["message_id"] != float64(17) {
					t.Fatal(got.params)
				}
			case "member.mute":
				until := int64(got.params["until_date"].(float64))
				if until < start+60 || until > time.Now().Unix()+60 || got.params["user_id"] != float64(5) {
					t.Fatal(got.params)
				}
				permissions := got.params["permissions"].(map[string]any)
				if len(permissions) != 10 {
					t.Fatal(permissions)
				}
				for _, value := range permissions {
					if value != false {
						t.Fatal(permissions)
					}
				}
			}
			after, _ := json.Marshal(tc.params)
			if string(before) != string(after) {
				t.Fatal("caller params mutated")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"unknown", nil}, {"message.recall", map[string]any{"chat_id": 9}},
		{"chat.leave", map[string]any{"chat_id": 1.5}},
		{"member.mute", map[string]any{"chat_id": 9, "user_id": 5, "duration_seconds": 0}},
	} {
		if _, err := a.CallCapability(context.Background(), tc.name, tc.params); err == nil {
			t.Fatalf("accepted invalid %s %#v", tc.name, tc.params)
		}
	}
	select {
	case got := <-requests:
		t.Fatalf("invalid request sent: %#v", got)
	default:
	}
	params := map[string]any{"custom": "value"}
	if _, err := a.CallPlatformAPI(context.Background(), "chat.leave", params); err != nil {
		t.Fatal(err)
	}
	got := awaitTelegram(t, requests)
	if got.method != "chat.leave" || !reflect.DeepEqual(got.params, params) {
		t.Fatalf("native API was mapped: %#v", got)
	}
}
