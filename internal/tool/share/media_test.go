package share

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMediaInputRejectsWrongTypesAndExtraFields(t *testing.T) {
	for _, raw := range []string{`[]`, `{"media":1}`, `{"media":{}}`, `{"media_id":"media:` + strings.Repeat("a", 64) + `"}`, `{"media":"media:` + strings.Repeat("a", 64) + `","path":"escape"}`} {
		var input MediaInput
		if err := json.Unmarshal([]byte(raw), &input); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestMediaInputLeavesIDValidationToMedia(t *testing.T) {
	var input MediaInput
	if err := json.Unmarshal([]byte(`{"media":"bad"}`), &input); err != nil || input.MediaID != "bad" {
		t.Fatalf("input = %+v, %v", input, err)
	}
}
