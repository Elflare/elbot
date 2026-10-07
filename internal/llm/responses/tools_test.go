package responses

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAdditionalToolsPreservesDefinitionsAndRejectsInvalidItems(t *testing.T) {
	tools := []FunctionTool{{Name: "read", Description: "stable definition", Parameters: map[string]any{"type": "object", "maximum": json.Number("9007199254740993")}}}
	item, err := AdditionalTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	tools[0].Parameters["maximum"] = 1
	decoded, err := AdditionalToolDefinitions(item)
	if err != nil || decoded[0].Parameters["maximum"] != json.Number("9007199254740993") || decoded[0].Strict {
		t.Fatalf("definitions=%+v err=%v", decoded, err)
	}
	for _, raw := range []string{
		`{"type":"message","role":"developer","tools":[{"type":"function","name":"read"}]}`,
		`{"type":"additional_tools","role":"user","tools":[{"type":"function","name":"read"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":""}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"web_search","name":"read"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"read"},{"type":"function","name":"read"}]}`,
	} {
		item, err := ParseItem([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AdditionalToolDefinitions(item); err == nil {
			t.Fatalf("accepted invalid definitions: %s", raw)
		}
	}
}

func TestPreparedToolChoiceCannotExpandRoutePermissions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured any
		allowed    []string
		want       string
		names      []string
		wantErr    string
	}{
		{name: "default", allowed: []string{"write", "read", "read"}, want: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"read"},{"type":"function","name":"write"}]}`, names: []string{"read", "write"}},
		{name: "empty", want: `"none"`},
		{name: "disabled", configured: "none", allowed: []string{"read"}, want: `"none"`},
		{name: "required", configured: "required", allowed: []string{"read"}, want: `{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"read"}]}`, names: []string{"read"}},
		{name: "forced", configured: map[string]any{"type": "function", "name": "read"}, allowed: []string{"read", "write"}, want: `{"type":"function","name":"read"}`, names: []string{"read"}},
		{name: "intersection", configured: map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{map[string]any{"type": "function", "name": "read"}, map[string]any{"type": "function", "name": "hidden"}}}, allowed: []string{"read", "write"}, want: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"read"}]}`, names: []string{"read"}},
		{name: "empty intersection", configured: map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{map[string]any{"type": "function", "name": "hidden"}}}, allowed: []string{"read"}, want: `"none"`},
		{name: "required unavailable", configured: "required", wantErr: "no functions"},
		{name: "forced unavailable", configured: map[string]any{"type": "function", "name": "hidden"}, allowed: []string{"read"}, wantErr: "unavailable"},
		{name: "invalid mode", configured: map[string]any{"type": "allowed_tools", "mode": "none"}, wantErr: "mode"},
		{name: "hosted tool", configured: map[string]any{"type": "web_search"}, wantErr: "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var extras map[string]any
			if tc.configured != nil {
				extras = map[string]any{"tool_choice": tc.configured}
			}
			client := mustClient(t, "http://unused.invalid", extras, nil, RequestOptions{})
			prepared, err := client.PrepareRequest(Request{Model: "m", AllowedTools: tc.allowed})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v want=%s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(prepared.JSON(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body["tool_choice"]) != tc.want || !reflect.DeepEqual(prepared.AllowedTools(), tc.names) {
				t.Fatalf("choice=%s allowed=%v", body["tool_choice"], prepared.AllowedTools())
			}
			if _, exists := body["tools"]; exists {
				t.Fatal("top-level tools present")
			}
		})
	}
}
