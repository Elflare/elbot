package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestCompactionResetsElyphRulesForNewSession(t *testing.T) {
	raw, err := CompactedMetadata(`{"elyph_rules_shown":true,"tool_tags":["keep"],"unknown":9007199254740993}`, &CompactState{Pending: true, Summary: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "elyph_rules_shown") || !strings.Contains(raw, `"tool_tags":["keep"]`) || !strings.Contains(raw, "9007199254740993") {
		t.Fatalf("new session state: %s", raw)
	}
}

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
	if err != nil || restored.TotalTokens == nil || *restored.TotalTokens != 3 {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	total, cache := int64(12), int64(4)
	usage := &llm.Usage{TotalTokens: &total, CacheHitTokens: &cache}
	if err := service.RecordTurn(ctx, row.ID, usage); err != nil {
		t.Fatal(err)
	}
	*usage.TotalTokens = 999
	snapshot, _ := service.Usage(row)
	if snapshot.TotalTokens == nil || *snapshot.TotalTokens != 12 {
		t.Fatal("usage aliases input")
	}
	*snapshot.TotalTokens = 888
	snapshot, _ = service.Usage(row)
	if snapshot.TotalTokens == nil || *snapshot.TotalTokens != 12 {
		t.Fatal("usage aliases output")
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	restored, _ = New(Options{Store: store}).Usage(latest)
	if restored.TotalTokens == nil || *restored.TotalTokens != 12 || !strings.Contains(latest.Metadata, `"tool_tags":["keep"]`) {
		t.Fatalf("restore=%v metadata=%s", restored, latest.Metadata)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	total = 20
	if err := service.RecordTurn(canceled, row.ID, &llm.Usage{TotalTokens: &total}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save error=%v", err)
	}
	observed, _ := service.Usage(row)
	persisted, _ := store.Sessions().Get(ctx, row.ID)
	if observed.TotalTokens == nil || *observed.TotalTokens != 20 || persisted.Metadata != latest.Metadata || !persisted.UpdatedAt.Equal(latest.UpdatedAt) {
		t.Fatalf("observation=%v persisted=%s", observed, persisted.Metadata)
	}
}

func TestRecordTurnWithoutUsagePreservesStoredAndObservedUsage(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Metadata: `{"unknown":9007199254740993,"last_usage":{"TotalTokens":3}}`, UpdatedAt: time.Now().Add(-time.Hour)}
	if err := store.Sessions().Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Store: store})
	if err := service.RecordTurn(t.Context(), row.ID, nil); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || !latest.UpdatedAt.After(row.UpdatedAt) || latest.Metadata != row.Metadata {
		t.Fatalf("activity update changed metadata: %+v error=%v", latest, err)
	}
	usage, err := service.Usage(latest)
	if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 3 {
		t.Fatalf("stored usage lost: %+v error=%v", usage, err)
	}
	tokens := int64(9)
	if err := service.RecordTurn(t.Context(), row.ID, &llm.Usage{TotalTokens: &tokens}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordTurn(t.Context(), row.ID, nil); err != nil {
		t.Fatal(err)
	}
	latest, err = store.Sessions().Get(t.Context(), row.ID)
	if err != nil || !strings.Contains(latest.Metadata, "9007199254740993") {
		t.Fatalf("unknown metadata lost: %+v error=%v", latest, err)
	}
	for _, restored := range []*Service{service, New(Options{Store: store})} {
		usage, err := restored.Usage(latest)
		if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 9 {
			t.Fatalf("nil usage erased snapshot: %+v error=%v", usage, err)
		}
	}
}

func TestRecordTurnInvalidContextRollsBackActivityAndUsage(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Metadata: `{"context_compact":"invalid","last_usage":{"TotalTokens":3}}`, UpdatedAt: time.Now().Add(-time.Hour)}
	if err := store.Sessions().Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Store: store})
	tokens := int64(9)
	if err := service.RecordTurn(t.Context(), row.ID, &llm.Usage{TotalTokens: &tokens}); err == nil {
		t.Fatal("invalid context was accepted")
	}
	latest, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || latest.Metadata != row.Metadata || !latest.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("failed transaction changed session: %+v error=%v", latest, err)
	}
	usage, err := service.Usage(latest)
	if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 9 {
		t.Fatalf("observed usage lost after rollback: %+v error=%v", usage, err)
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
