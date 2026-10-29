// 验证 hook.v2 帧编解码和请求标识。

package protocol

import (
	"encoding/json"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/hook/share"
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
