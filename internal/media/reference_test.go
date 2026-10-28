package media

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestArgumentMediaIDsUsesExactJSONValues(t *testing.T) {
	first := IDPrefix + strings.Repeat("a", 64)
	second := IDPrefix + strings.Repeat("b", 64)
	keyOnly := IDPrefix + strings.Repeat("c", 64)
	tests := []struct {
		name      string
		arguments string
		want      []string
	}{
		{
			name:      "nested values sorted and deduplicated",
			arguments: fmt.Sprintf(`{"send_media":%q,"images":[%q,{"nested":[%q,%q]}],%q:"ignored","prompt":%q,"count":1,"enabled":true,"empty":null}`, second, first, second, first, keyOnly, "prefix "+keyOnly),
			want:      []string{first, second},
		},
		{"root string", fmt.Sprintf("%q", first), []string{first}},
		{"object key only", fmt.Sprintf(`{%q:"ignored"}`, keyOnly), nil},
		{"embedded ID", fmt.Sprintf(`{"prompt":%q}`, "prefix "+first), nil},
		{"invalid ID", `{"source":"media:short"}`, nil},
		{"invalid JSON", `{"source":`, nil},
		{"trailing JSON", fmt.Sprintf(`{"source":%q} {}`, first), nil},
		{"null", `null`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ArgumentMediaIDs(tt.arguments); !slices.Equal(got, tt.want) {
				t.Fatalf("ArgumentMediaIDs(%q) = %v, want %v", tt.arguments, got, tt.want)
			}
		})
	}
}
