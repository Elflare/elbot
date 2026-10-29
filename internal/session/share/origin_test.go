package share_test

import (
	"testing"

	"elbot/internal/llm"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestOriginReadsSavedFacts(t *testing.T) {
	for _, row := range []*storage.Session{nil, {}, {Metadata: `{"unknown":true}`}} {
		if got, known, err := share.Origin(row); err != nil || known || got != (llm.Origin{}) {
			t.Fatalf("missing origin = %+v, %v, %v", got, known, err)
		}
	}
	for _, tc := range []struct {
		metadata string
		want     llm.Origin
	}{
		{`{"llm_origin":{"protocol":"chat"}}`, llm.Origin{APIType: llm.APITypeChat}},
		{`{"llm_origin":{"protocol":"response","provider":"historical","base_url":"https://old.invalid"}}`, llm.Origin{APIType: llm.APITypeResponse, Provider: "historical", BaseURL: "https://old.invalid"}},
	} {
		if got, known, err := share.Origin(&storage.Session{Metadata: tc.metadata}); err != nil || !known || got != tc.want {
			t.Fatalf("origin = %+v, %v, %v; want %+v", got, known, err, tc.want)
		}
	}
	for _, metadata := range []string{`{broken`, `null`, `[]`, `{"llm_origin":null}`, `{"llm_origin":{}}`, `{"llm_origin":"chat"}`, `{"llm_origin":{"protocol":7}}`} {
		if _, known, err := share.Origin(&storage.Session{Metadata: metadata}); err == nil || known {
			t.Fatalf("invalid origin accepted: %s", metadata)
		}
	}
}

func TestInheritOriginPreservesTargetMetadataOnly(t *testing.T) {
	source := &storage.Session{Metadata: `{"llm_origin":{"protocol":"chat","provider":"historical"},"last_usage":{"total_tokens":12}}`}
	target := `{"unknown":9007199254740993,"nested":{"keep":true},"llm_origin":{"protocol":"response","provider":"wrong"}}`
	got, err := share.InheritOrigin(source, target)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(got)
	if err != nil {
		t.Fatal(err)
	}
	origin, known, err := share.Origin(&storage.Session{Metadata: got})
	if err != nil || !known || origin.APIType != llm.APITypeChat || origin.Provider != "historical" || string(fields["unknown"]) != "9007199254740993" || string(fields["nested"]) != `{"keep":true}` || fields["last_usage"] != nil {
		t.Fatalf("incorrect inheritance: %s, %v", got, err)
	}
	if source.Metadata != `{"llm_origin":{"protocol":"chat","provider":"historical"},"last_usage":{"total_tokens":12}}` {
		t.Fatal("inheritance mutated source")
	}
	got, err = share.InheritOrigin(nil, got)
	if err != nil {
		t.Fatal(err)
	}
	fields, err = storage.DecodeSessionMetadata(got)
	if err != nil || fields["llm_origin"] != nil || string(fields["unknown"]) != "9007199254740993" {
		t.Fatalf("missing source retained target ownership or lost metadata: %s, %v", got, err)
	}
	for _, tc := range []struct{ source, target string }{
		{`{broken`, target},
		{source.Metadata, `{broken`},
	} {
		if _, err := share.InheritOrigin(&storage.Session{Metadata: tc.source}, tc.target); err == nil {
			t.Fatal("invalid metadata accepted during inheritance")
		}
	}
}
