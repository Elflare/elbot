package builtin

import "testing"

func TestToolsString(t *testing.T) {
	if got := toolsString(map[string]int{"z": 2, "a": 1}); got != "a x1, z x2" {
		t.Fatalf("tool summary = %q", got)
	}
	if got := toolsString(nil); got != "" {
		t.Fatalf("empty summary = %q", got)
	}
}
