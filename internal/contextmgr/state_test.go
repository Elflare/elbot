package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestUsageSnapshotsRestoreAndSaveFailure(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/usage.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Metadata: `{"tool_tags":["keep"],"last_usage":{"TotalTokens":3}}`}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Store: store})
	restored, err := service.Usage(row)
	if err != nil || restored.TotalTokens != 3 {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	usage := &llm.Usage{TotalTokens: 12, CacheHitTokens: 4}
	if err := service.RecordUsage(ctx, row.ID, usage); err != nil {
		t.Fatal(err)
	}
	usage.TotalTokens = 999
	snapshot, _ := service.Usage(row)
	if snapshot.TotalTokens != 12 {
		t.Fatal("usage aliases input")
	}
	snapshot.TotalTokens = 888
	snapshot, _ = service.Usage(row)
	if snapshot.TotalTokens != 12 {
		t.Fatal("usage aliases output")
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	restored, _ = New(Options{Store: store}).Usage(latest)
	if restored.TotalTokens != 12 || !strings.Contains(latest.Metadata, `"tool_tags":["keep"]`) {
		t.Fatalf("restore=%v metadata=%s", restored, latest.Metadata)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := service.RecordUsage(canceled, row.ID, &llm.Usage{TotalTokens: 20}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save error=%v", err)
	}
	observed, _ := service.Usage(row)
	persisted, _ := store.Sessions().Get(ctx, row.ID)
	if observed.TotalTokens != 20 || persisted.Metadata != latest.Metadata {
		t.Fatalf("observation=%v persisted=%s", observed, persisted.Metadata)
	}
}

func TestSeedMutationOwnsOnlyContextFields(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/seed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := `{"context_compact":{"pending":true,"summary":"seed"},"last_usage":{"TotalTokens":40},"tool_tags":["t"],"unknown":9007199254740993,"title_source":"manual","workspace":{"dir":"work"}}`
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Metadata: original}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Store: store})
	seed, err := PendingCompact(row)
	if err != nil || seed == nil {
		t.Fatalf("seed=%v err=%v", seed, err)
	}
	seed.Summary = "caller mutation"
	saved, err := service.ConsumeSeed(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(saved.Metadata)
	if err != nil || state.Compact.Pending || state.Compact.Summary != "seed" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	for _, want := range []string{"9007199254740993", `"tool_tags":["t"]`, `"title_source":"manual"`, `"workspace":{"dir":"work"}`} {
		if !strings.Contains(saved.Metadata, want) {
			t.Fatalf("lost %s in %s", want, saved.Metadata)
		}
	}
	next, err := CompactedMetadata(saved.Metadata, &CompactState{Pending: true, Summary: "new"})
	if err != nil || strings.Contains(next, "last_usage") || !strings.Contains(next, "9007199254740993") {
		t.Fatalf("handoff=%s err=%v", next, err)
	}
	for _, raw := range []string{`{"context_compact":"bad","tool_tags":["keep"]}`, `{"last_usage":"bad"}`, `{bad`} {
		if _, err := CompactedMetadata(raw, &CompactState{Summary: "new"}); err == nil {
			t.Fatalf("accepted bad metadata %s", raw)
		}
	}
}
