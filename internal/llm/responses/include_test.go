package responses

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRequiredIncludeMergesWithoutMutatingConfiguration(t *testing.T) {
	configured := []string{"message.output_text.logprobs", "reasoning.encrypted_content"}
	extras := map[string]any{"include": configured}
	client := mustClient(t, "https://test.invalid", extras, nil, RequestOptions{})
	required := []string{"reasoning.encrypted_content"}
	prepared, err := client.PrepareRequest(Request{Model: "m", Include: required})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Include []string `json:"include"`
	}
	if err := json.Unmarshal(prepared.body, &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body.Include, []string{"reasoning.encrypted_content", "message.output_text.logprobs"}) {
		t.Fatalf("include=%v", body.Include)
	}
	if !reflect.DeepEqual(configured, []string{"message.output_text.logprobs", "reasoning.encrypted_content"}) || len(required) != 1 {
		t.Fatal("configuration mutated")
	}
	configured[0], required[0] = "changed", "changed"
	var frozen struct {
		Include []string `json:"include"`
	}
	if err := json.Unmarshal(prepared.body, &frozen); err != nil || !reflect.DeepEqual(body.Include, frozen.Include) {
		t.Fatalf("prepared request changed: %v %v", frozen.Include, err)
	}
}

func TestRequiredIncludeRejectsMalformedAndConflictingExtras(t *testing.T) {
	for _, value := range []any{nil, "reasoning.encrypted_content", []any{1}, []string{""}} {
		client := mustClient(t, "https://test.invalid", map[string]any{"include": value}, nil, RequestOptions{})
		if _, err := client.PrepareRequest(Request{Model: "m", Include: []string{"reasoning.encrypted_content"}}); err == nil {
			t.Fatalf("malformed include accepted: %#v", value)
		}
	}
	client := mustClient(t, "https://test.invalid", map[string]any{"include": []string{"reasoning.encrypted_content"}}, map[string]map[string]any{"m": {"include": []string{"message.output_text.logprobs"}}}, RequestOptions{})
	if _, err := client.PrepareRequest(Request{Model: "m", Include: []string{"reasoning.encrypted_content"}}); err == nil {
		t.Fatal("conflicting configuration sources accepted")
	}
}
