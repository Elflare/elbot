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
		{"new", llm.Origin{}, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a"}, true},
		{"chat vendors", llm.Origin{Protocol: llm.ProtocolChat, Provider: "a"}, llm.Origin{Protocol: llm.ProtocolChat, Provider: "b"}, true},
		{"migrated chat", llm.Origin{Protocol: llm.ProtocolChat}, llm.Origin{Protocol: llm.ProtocolChat, Provider: "b"}, true},
		{"same vendor different endpoint", llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a", BaseURL: "https://old.invalid"}, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a", BaseURL: "https://new.invalid"}, true},
		{"response vendors", llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a"}, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "b"}, false},
		{"chat to response", llm.Origin{Protocol: llm.ProtocolChat, Provider: "a"}, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a"}, false},
		{"response to chat", llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a"}, llm.Origin{Protocol: llm.ProtocolChat, Provider: "a"}, false},
		{"missing response vendor", llm.Origin{Protocol: llm.ProtocolResponse}, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "a"}, false},
		{"unknown target", llm.Origin{}, llm.Origin{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CanSwitch(tc.source, tc.target); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}
