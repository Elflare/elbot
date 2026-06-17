package background

import "testing"

func TestParseJSONResultAcceptsPlainJSON(t *testing.T) {
	result, err := ParseJSONResult(`{"completed":true,"need_report":false,"report":"ok"}`)
	if err != nil {
		t.Fatalf("ParseJSONResult: %v", err)
	}
	if !result.Completed || result.NeedReport || result.Report != "ok" {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseJSONResultAcceptsCodeFenceAndExtraText(t *testing.T) {
	result, err := ParseJSONResult("前缀```json\n{\"completed\":true,\"need_report\":true,\"report\":\"ok\"}\n```后缀")
	if err != nil {
		t.Fatalf("ParseJSONResult: %v", err)
	}
	if !result.Completed || !result.NeedReport || result.Report != "ok" {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseJSONResultRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseJSONResult("not json"); err == nil {
		t.Fatal("expected error")
	}
}
