package protocol

import (
	"encoding/json"
	"testing"
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
