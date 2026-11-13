// 验证 hook.v2 帧编解码和请求标识。

package protocol

import (
	"encoding/json"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/hook/share"
	"elbot/internal/operationresult"
)

func TestFrameRoundTripAndIDValidation(t *testing.T) {
	ok := true
	want := Frame{Type: "response", ID: "host:event", OK: &ok, Result: json.RawMessage(`{"status":"completed"}`)}
	data, err := EncodeFrame(want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	got, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if got.Type != want.Type || got.ID != want.ID || got.OK == nil || !*got.OK || string(got.Result) != string(want.Result) {
		t.Fatalf("Frame = %#v", got)
	}
	if err := ValidateID(got.ID, "host:"); err != nil {
		t.Fatalf("ValidateID: %v", err)
	}
	if err := ValidateID("plugin:event", "host:"); err == nil {
		t.Fatal("ValidateID accepted wrong prefix")
	}
}

func TestNewRequestEncodesParams(t *testing.T) {
	request, err := NewRequest("host:event", "event.handle", map[string]string{"value": "ok"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if request.Type != "request" || request.ID != "host:event" || request.Method != "event.handle" || string(request.Params) != `{"value":"ok"}` {
		t.Fatalf("request = %#v", request)
	}
}

func TestEventOutputDeliveryInfo(t *testing.T) {
	out := delivery.Emoticon("14", "微笑")
	out.Delivery = delivery.DeliveryInfo{Timing: delivery.DeliveryAfterAssistant, HookPoint: "llm.response.received", HookName: "emoticons", HookMode: "run"}
	request, err := NewRequest("host:event", "event.handle", EventHandleParams{Event: share.Event{Outputs: []delivery.Output{out}}})
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Event struct {
			Outputs []map[string]json.RawMessage `json:"outputs"`
		} `json:"event"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Event.Outputs) != 1 {
		t.Fatalf("outputs = %#v", params.Event.Outputs)
	}
	output := params.Event.Outputs[0]
	if got, want := string(output["Delivery"]), `{"Timing":"after_assistant","HookPoint":"llm.response.received","HookName":"emoticons","HookMode":"run"}`; got != want {
		t.Fatalf("Delivery = %s, want %s", got, want)
	}
	if string(output["Name"]) != `"微笑"` || string(output["Text"]) != `""` {
		t.Fatalf("emoticon = %#v", output)
	}
	for _, key := range []string{"Meta", "AltText"} {
		if _, exists := output[key]; exists {
			t.Fatalf("unexpected field %s in output", key)
		}
	}
}

func TestEventToolResultKeepsWireTextShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		tool   share.ToolPayload
		result string
		err    string
	}{
		{name: "success", tool: share.ToolPayload{Result: operationresult.Result[string]{Status: operationresult.Success, Data: "done"}}, result: `"done"`},
		{name: "rejected", tool: share.ToolPayload{Result: operationresult.Result[string]{Status: operationresult.Rejected, Message: "tool unavailable"}}, result: `"tool unavailable"`},
		{name: "go error", tool: share.ToolPayload{Error: "internal failure"}, result: `"internal failure"`, err: `"internal failure"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(share.Event{Tool: test.tool})
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Tool json.RawMessage `json:"tool"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			var tool map[string]json.RawMessage
			if err := json.Unmarshal(decoded.Tool, &tool); err != nil {
				t.Fatal(err)
			}
			if string(tool["result"]) != test.result || string(tool["error"]) != test.err {
				t.Fatalf("tool payload = %s", decoded.Tool)
			}
			if _, ok := tool["status"]; ok {
				t.Fatalf("internal status leaked to wire: %s", decoded.Tool)
			}
		})
	}
}
