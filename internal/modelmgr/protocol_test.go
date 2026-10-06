package modelmgr

import (
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

func TestSelectionCarriesClientAndCompositionOriginsAreSnapshots(t *testing.T) {
	opts := testOptions()
	native := &testClient{}
	opts.Clients["p"] = native
	provider := opts.Providers["p"]
	provider.APIMode = "response"
	provider.BaseURL = "https://response.invalid/v1/"
	opts.Providers["p"] = provider
	s := newTestService(t, opts)
	selected := s.ResolveMode("work")
	if selected.Client != native || selected.Provider != "p" || selected.Model != "a" {
		t.Fatalf("selection=%+v", selected)
	}
	origins := s.ProviderOrigins()
	var found bool
	for i, origin := range origins {
		if origin.Provider == "p" {
			found = true
			if origin.Protocol != llm.ProtocolResponse || origin.BaseURL != "https://response.invalid/v1" {
				t.Fatalf("origin=%+v", origin)
			}
			origins[i].Protocol = llm.ProtocolChat
		}
	}
	if !found {
		t.Fatal("configured provider identity is missing")
	}
	provider.APIMode = "chat"
	opts.Providers["p"] = provider
	for _, origin := range s.ProviderOrigins() {
		if origin.Provider == "p" && origin.Protocol != llm.ProtocolResponse {
			t.Fatal("caller changed immutable composition facts")
		}
	}
	if s.Resolve(config.ModelSelection{Provider: "missing", Model: "m"}).Client != nil {
		t.Fatal("missing provider fabricated a client")
	}
}

func TestNewLeavesProtocolValidationToComposition(t *testing.T) {
	for _, mode := range []string{"custom", "test"} {
		t.Run(mode, func(t *testing.T) {
			opts := testOptions()
			provider := opts.Providers["p"]
			provider.APIMode = mode
			opts.Providers["p"] = provider
			service, err := New(opts)
			if err != nil {
				t.Fatalf("error=%v", err)
			}
			if service.ResolveMode("work").Client != opts.Clients["p"] {
				t.Fatal("custom protocol lost its configured client")
			}
		})
	}
}
