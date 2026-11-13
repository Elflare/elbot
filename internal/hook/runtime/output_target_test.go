package runtime

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/delivery"
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
			if err := registerHookTestTool(reg, loggingTool{run: func(context.Context, toolshare.CallRequest) (operationresult.Result[*toolshare.Result], string, error) {
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Outputs: tc.outputs}}, "", nil
			}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{reg}, Send: func(_ context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
				calls++
				if target != tc.want || !reflect.DeepEqual(outputs, tc.outputs) {
					t.Fatalf("target=%+v outputs=%+v", target, outputs)
				}
				return operationresult.Result[delivery.Receipt]{Status: operationresult.Success, Data: delivery.Receipt{}}, nil
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
			outcome, err := w.callTool(params)
			if tc.conflict {
				if err != nil || outcome.(operationresult.Result[map[string]any]).Status != operationresult.InvalidArguments || calls != 0 {
					t.Fatalf("conflict sent: calls=%d err=%v", calls, err)
				}
			} else if err != nil || outcome.(operationresult.Result[map[string]any]).Status != operationresult.Success || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
