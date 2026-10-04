package toolrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func stateTestStore(t *testing.T, metadata string) (storage.Store, *storage.Session) {
	t.Helper()
	store, err := sqlite.New(context.Background(), t.TempDir()+"/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Metadata: metadata}
	if err := store.Sessions().Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	return store, row
}

func stateTestTool(name string) CachedTool {
	return CachedTool{Name: name, Source: SourceKindNative, Schema: llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: name, Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
}

func TestStateCommitRestoresAndDetachesSchemas(t *testing.T) {
	ctx := context.Background()
	store, row := stateTestStore(t, `{"workspace":"keep","unknown":9007199254740993,"title_source":"manual","last_usage":{"TotalTokens":12}}`)
	service := NewStateService(store)
	item := stateTestTool("external")
	item.Source = SourceKindELwisp
	item.CanonicalName = "wisp/external"
	item.Endpoint = "http://example.invalid/tool"
	item.TimeoutSeconds = 7
	committed, err := service.Commit(ctx, row.ID, StateUpdate{Tools: []CachedTool{item}, Tags: []string{"tag"}, ShownRuleCardFormats: []string{"elyph"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(committed.Injected, ",") != "external" {
		t.Fatalf("commit=%+v", committed)
	}
	item.Schema.Function.Parameters["properties"].(map[string]any)["bad"] = true
	committed.State.ToolCache[0].Schema.Function.Parameters["bad"] = true
	restored, err := NewStateService(store).Snapshot(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached := restored.CachedTools(nil, false)
	if len(cached) != 1 || cached[0].CanonicalName != "wisp/external" || cached[0].Endpoint != item.Endpoint || cached[0].TimeoutSeconds != 7 {
		t.Fatalf("restored=%+v", cached)
	}
	if _, ok := cached[0].Schema.Function.Parameters["bad"]; ok {
		t.Fatal("output mutated persistence")
	}
	if _, ok := cached[0].Schema.Function.Parameters["properties"].(map[string]any)["bad"]; ok {
		t.Fatal("input mutated persistence")
	}
	cached[0].Schema.Function.Parameters["bad"] = true
	if _, ok := restored.ToolCache[0].Schema.Function.Parameters["bad"]; ok {
		t.Fatal("cache view aliases state")
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	for _, want := range []string{`"unknown":9007199254740993`, `"workspace":"keep"`, `"title_source":"manual"`, `"TotalTokens":12`} {
		if !strings.Contains(latest.Metadata, want) {
			t.Fatalf("missing %s in %s", want, latest.Metadata)
		}
	}
	repeat, err := service.Commit(ctx, row.ID, StateUpdate{Tools: []CachedTool{stateTestTool("external")}})
	if err != nil || len(repeat.Injected) != 0 || strings.Join(repeat.Existing, ",") != "external" {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
}

func TestStateCommitFailureKeepsAllFields(t *testing.T) {
	for _, metadata := range []string{`{"tool_tags":["old"],"unknown":5}`, `{"tool_cache":"invalid","unknown":5}`, `{broken`} {
		t.Run(metadata, func(t *testing.T) {
			ctx := context.Background()
			store, row := stateTestStore(t, metadata)
			invalid := stateTestTool("new")
			invalid.Schema.Function.Parameters["invalid"] = make(chan int)
			result, err := NewStateService(store).Commit(ctx, row.ID, StateUpdate{Tools: []CachedTool{invalid}, Tags: []string{"new"}, ShownRuleCardFormats: []string{"new"}})
			if err == nil || result.Metadata != "" || len(result.Injected) != 0 {
				t.Fatalf("failure published state: %+v %v", result, err)
			}
			latest, _ := store.Sessions().Get(ctx, row.ID)
			if latest.Metadata != metadata {
				t.Fatalf("failed transaction changed metadata: %s", latest.Metadata)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, row := stateTestStore(t, "")
	_, err := NewStateService(store).Commit(ctx, row.ID, StateUpdate{Tools: []CachedTool{stateTestTool("canceled")}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	latest, _ := store.Sessions().Get(context.Background(), row.ID)
	if latest.Metadata != "" {
		t.Fatal("canceled commit published state")
	}
}

func TestStateConcurrentUpdatesPreserveOtherOwners(t *testing.T) {
	ctx := context.Background()
	store, row := stateTestStore(t, `{"unknown":9007199254740993,"context_compact":{"pending":true,"summary":"seed"}}`)
	service := NewStateService(store)
	contexts := contextmgr.New(contextmgr.Options{Store: store})
	const count = 24
	start := make(chan struct{})
	errs := make(chan error, count*3)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := service.Commit(ctx, row.ID, StateUpdate{Tools: []CachedTool{stateTestTool(fmt.Sprintf("t%d", i))}, Tags: []string{fmt.Sprintf("tag%d", i)}})
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- contexts.RecordUsage(ctx, row.ID, &llm.Usage{TotalTokens: i + 1})
		}(i)
		go func() {
			defer wg.Done()
			<-start
			snapshot, err := service.Snapshot(ctx, row.ID)
			if err == nil {
				for _, item := range snapshot.ToolCache {
					item.Schema.Function.Parameters["reader"] = true
				}
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := service.Snapshot(ctx, row.ID)
	if err != nil || len(state.ToolCache) != count || len(state.ToolTags) != count {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	contextsState, err := contextmgr.DecodeState(latest.Metadata)
	if err != nil || contextsState.LastUsage == nil || contextsState.Compact == nil || contextsState.Compact.Summary != "seed" {
		t.Fatalf("context=%+v err=%v", contextsState, err)
	}
	usage, _ := contexts.Usage(latest)
	if *usage != *contextsState.LastUsage || !strings.Contains(latest.Metadata, "9007199254740993") {
		t.Fatalf("lost committed state: %s", latest.Metadata)
	}
	if strings.Contains(latest.Metadata, "reader") {
		t.Fatal("reader mutated persisted schema")
	}
}

func TestSchemasAreDetachedForPreparedHook(t *testing.T) {
	item := stateTestTool("tool")
	manager := NewManager(nil, nil)
	schemas, err := manager.Schemas(context.Background(), Context{}, []CachedTool{item})
	if err != nil {
		t.Fatal(err)
	}
	schemas[0].Function.Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] = "number"
	next, err := manager.Schemas(context.Background(), Context{}, []CachedTool{item})
	if err != nil {
		t.Fatal(err)
	}
	if next[0].Function.Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Fatal("prepared hook polluted next request")
	}
}
