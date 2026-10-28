package modelmgr

import (
	"elbot/internal/config"
	"elbot/internal/llm"
	"testing"
)

func TestSelectionOriginIsAnIndependentSnapshot(t *testing.T) {
	opts := testOptions()
	p := opts.Providers["p"]
	p.APIMode = "response"
	p.BaseURL = "https://response.invalid/v1/"
	opts.Providers["p"] = p
	s := newTestService(t, opts)
	selected := s.ResolveMode("work")
	if selected.Client != opts.Clients["p"] || selected.Origin.APIType != llm.APITypeResponse || selected.Origin.BaseURL != "https://response.invalid/v1" {
		t.Fatal(selected)
	}
	selected.Origin.APIType = llm.APITypeChat
	p.APIMode = "chat"
	opts.Providers["p"] = p
	if s.ResolveMode("work").Origin.APIType != llm.APITypeResponse {
		t.Fatal("origin changed")
	}
	if s.Resolve(config.ModelSelection{Provider: "missing", Model: "m"}).Client != nil {
		t.Fatal("missing provider fabricated client")
	}
}
