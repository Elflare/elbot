package modelmgr

import (
	"testing"

	"elbot/internal/llm"
)

func TestMaterialCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		source, target llm.Origin
		allowed        bool
	}{
		{"new", llm.Origin{}, llm.Origin{APIType: llm.APITypeResponse, Provider: "a"}, true},
		{"chat vendors", llm.Origin{APIType: llm.APITypeChat, Provider: "a"}, llm.Origin{APIType: llm.APITypeChat, Provider: "b"}, true},
		{"migrated chat", llm.Origin{APIType: llm.APITypeChat}, llm.Origin{APIType: llm.APITypeChat, Provider: "b"}, true},
		{"same vendor different endpoint", llm.Origin{APIType: llm.APITypeResponse, Provider: "a", BaseURL: "https://old.invalid"}, llm.Origin{APIType: llm.APITypeResponse, Provider: "a", BaseURL: "https://new.invalid"}, true},
		{"response vendors", llm.Origin{APIType: llm.APITypeResponse, Provider: "a"}, llm.Origin{APIType: llm.APITypeResponse, Provider: "b"}, false},
		{"chat to response", llm.Origin{APIType: llm.APITypeChat, Provider: "a"}, llm.Origin{APIType: llm.APITypeResponse, Provider: "a"}, false},
		{"response to chat", llm.Origin{APIType: llm.APITypeResponse, Provider: "a"}, llm.Origin{APIType: llm.APITypeChat, Provider: "a"}, false},
		{"missing response vendor", llm.Origin{APIType: llm.APITypeResponse}, llm.Origin{APIType: llm.APITypeResponse, Provider: "a"}, false},
		{"unknown target", llm.Origin{}, llm.Origin{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CanSwitch(tc.source, tc.target); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}
