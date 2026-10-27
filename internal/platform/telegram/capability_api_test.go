// capability_api_test.go 验证原生 HTTP 请求、参数与结果透传。
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
)

func TestNativeAPIPassthrough(t *testing.T) {
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
	params := map[string]any{"custom": "value"}
	result, err := a.CallPlatformAPI(context.Background(), "custom_api", params)
	if err != nil || string(result) != `{"accepted":true}` {
		t.Fatalf("result=%s err=%v", result, err)
	}
	got := awaitTelegram(t, requests)
	if got.method != "custom_api" || !reflect.DeepEqual(got.params, params) {
		t.Fatalf("native API was mapped: %#v", got)
	}
}
