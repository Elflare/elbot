package elvena

import (
	"encoding/json"
	"testing"

	"elbot/internal/tool"
)

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
