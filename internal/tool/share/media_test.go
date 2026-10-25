package share

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMediaInputRejectsInvalidAndExtraFields(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"media_id":"media:` + strings.Repeat("a", 64) + `"}`, `{"media":"bad"}`, `{"media":"media:` + strings.Repeat("a", 64) + `","path":"escape"}`} {
		var input MediaInput
		if err := json.Unmarshal([]byte(raw), &input); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
