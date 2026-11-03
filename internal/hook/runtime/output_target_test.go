package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

func TestToolOutputResolvesTargetBeforeSending(t *testing.T) {
	group := delivery.Target{Platform: "qqonebot", ChatType: "group", ChatID: "B"}
	explicit := delivery.Target{Platform: "telegram", Superadmins: true}
	for _, tc := range []struct {
		name     string
		target   delivery.Target
		outputs  []delivery.Output
		want     delivery.Target
		conflict bool
	}{
		{name: "current", outputs: []delivery.Output{delivery.Text("result")}},
		{name: "output target", outputs: []delivery.Output{{Kind: delivery.KindText, Target: group}}, want: group},
		{name: "explicit overrides", target: explicit, outputs: []delivery.Output{{Kind: delivery.KindText, Target: group}}, want: explicit},
		{name: "conflict", outputs: []delivery.Output{{Kind: delivery.KindText, Target: group}, {Kind: delivery.KindText, Target: explicit}}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.NewRegistry(security.DefaultPermission())
			if err := registerHookTestTool(reg, loggingTool{run: func(context.Context, toolshare.CallRequest) (*toolshare.Result, string, error) {
				return &toolshare.Result{Outputs: tc.outputs}, "", nil
			}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{reg}, Send: func(_ context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				calls++
				if target != tc.want || !reflect.DeepEqual(outputs, tc.outputs) {
					t.Fatalf("target=%+v outputs=%+v", target, outputs)
				}
				return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			closeRuntimeAfterRelease(t, manager, func() {})
			w := newWorker(manager, share.RuntimeConfig{ID: "test", EventTimeoutSeconds: 30, Tools: share.RuntimeToolsConfig{Allow: []string{"logging_tool"}}})
			token := testToolContext(t, w, t.Context(), share.Event{})
			params, err := json.Marshal(map[string]any{"name": "logging_tool", "tool_context": token, "target": tc.target})
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.callTool(params)
			if tc.conflict {
				if err == nil || calls != 0 {
					t.Fatalf("conflict sent: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
