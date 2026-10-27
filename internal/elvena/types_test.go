package elvena

import (
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/tool"
)

func TestNativeCallJSONRoundTrip(t *testing.T) {
	const wire = `{"calls":[{"platform":"telegram","api":"custom_api","params":{"nested":{"enabled":true},"values":[42,"id",null]}},{"api":"other_api"}]}`
	var req Request
	if err := json.Unmarshal([]byte(wire), &req); err != nil {
		t.Fatal(err)
	}
	want := []Call{
		{Platform: "telegram", API: "custom_api", Params: map[string]any{
			"nested": map[string]any{"enabled": true}, "values": []any{float64(42), "id", nil},
		}},
		{API: "other_api"},
	}
	if !reflect.DeepEqual(req.Calls, want) {
		t.Fatalf("decoded calls: %#v", req.Calls)
	}
	data, err := json.Marshal(req.Calls)
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Calls json.RawMessage `json:"calls"`
	}
	if err := json.Unmarshal([]byte(wire), &expected); err != nil {
		t.Fatal(err)
	}
	if string(data) != string(expected.Calls) {
		t.Fatalf("call wire format changed: %s", data)
	}
}

func TestRequestToolDeclarationJSON(t *testing.T) {
	const declaration = `{"name":"remote","description":"description","schema":{"type":"object"},"timeout_seconds":10,"endpoint":"https://example.invalid","foreground_only":true}`
	var req Request
	if err := json.Unmarshal([]byte(`{"tools":[`+declaration+`]}`), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 {
		t.Fatal(req.Tools)
	}
	got := req.Tools[0]
	if got.Name != "remote" || got.Description != "description" || got.Schema["type"] != "object" || got.TimeoutSeconds != 10 || got.Endpoint != "https://example.invalid" || !got.ForegroundOnly {
		t.Fatalf("lost protocol fields: %+v", got)
	}
	got.CanonicalName, got.EventKey = "internal_name", "internal_event"
	data, err := json.Marshal(got)
	if err != nil || string(data) != declaration {
		t.Fatalf("declaration JSON changed: %s err=%v", data, err)
	}
	data, err = json.Marshal(tool.ELwispToolDeclaration{Name: "minimal"})
	if err != nil || string(data) != `{"name":"minimal"}` {
		t.Fatalf("empty optional fields should be omitted: %s err=%v", data, err)
	}
}

func TestRequestToolDeclarationIgnoresInternalJSONFields(t *testing.T) {
	var req Request
	err := json.Unmarshal([]byte(`{"tools":[{"name":"remote","CanonicalName":"spoofed","EventKey":"spoofed","canonical_name":"spoofed","event_key":"spoofed"}]}`), &req)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 || req.Tools[0].CanonicalName != "" || req.Tools[0].EventKey != "" {
		t.Fatalf("internal fields accepted from protocol: %+v", req.Tools)
	}
}
