package modelmgr

import (
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type protocolTestClient struct {
	testClient
	protocol llm.ProtocolID
}

func (c *protocolTestClient) Protocol() llm.ProtocolID { return c.protocol }

func TestSelectionCarriesConfiguredClientAndProtocol(t *testing.T) {
	opts := testOptions()
	native := &protocolTestClient{protocol: llm.ProtocolResponse}
	opts.Clients["p"] = native
	provider := opts.Providers["p"]
	provider.APIMode = "response"
	opts.Providers["p"] = provider
	s := newTestService(t, opts)
	selected := s.ResolveMode("work")
	if selected.Client != native || selected.Protocol != llm.ProtocolResponse || selected.Provider != "p" || selected.Model != "a" {
		t.Fatalf("selection=%+v", selected)
	}
	if s.ResolveMode("chat").Protocol != llm.ProtocolChat {
		t.Fatal("default Chat protocol lost")
	}
	if s.Resolve(config.ModelSelection{Provider: "missing", Model: "m"}).Protocol != "" {
		t.Fatal("missing client fabricated a protocol")
	}
}

func TestNewRejectsProtocolMismatchAndInvalidAPIMode(t *testing.T) {
	for _, mode := range []string{"response", "responses"} {
		t.Run(mode, func(t *testing.T) {
			opts := testOptions()
			provider := opts.Providers["p"]
			provider.APIMode = mode
			opts.Providers["p"] = provider
			_, err := New(opts)
			if err == nil || !strings.Contains(err.Error(), "api_mode") || !strings.Contains(err.Error(), "p") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
