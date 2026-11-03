package state

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	toolregistry "elbot/internal/tool/registry"
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
	row := &storage.Session{ID: storage.NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", Mode: storage.SessionModeWork, Metadata: metadata, ChatType: "private"}
	if err := store.Sessions().Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	return store, row
}

func stateTestTool(name string) toolshare.DiscoveredTool {
	return toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: name, Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
}

func stateTestSnapshot(ctx context.Context, store storage.Store, id string) (toolshare.State, error) {
	row, err := store.Sessions().Get(ctx, id)
	if err != nil {
		return toolshare.State{}, err
	}
	return DecodeState(row.Metadata)
}

func TestStateCommitRestoresAndDetachesSchemas(t *testing.T) {
	ctx := context.Background()
	store, row := stateTestStore(t, `{"workspace":"keep","unknown":9007199254740993,"title_source":"manual","last_usage":{"TotalTokens":12}}`)
	service := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	item := stateTestTool("external")
	item.Source = toolshare.SourceKindELwisp
	item.CanonicalName = "wisp/external"
	item.Endpoint = "http://example.invalid/tool"
	item.TimeoutSeconds = 7
	committed, err := service.Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{item}, Tags: []string{"tag"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(committed.Injected, ",") != "wisp/external" {
		t.Fatalf("commit=%+v", committed)
	}
	repeated, err := service.Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{item}, Tags: []string{"tag"}})
	if err != nil || len(repeated.Injected) != 0 || strings.Join(repeated.Existing, ",") != "wisp/external" || repeated.Metadata != committed.Metadata {
		t.Fatalf("repeated commit=%+v err=%v", repeated, err)
	}
	item.Schema.Parameters["properties"].(map[string]any)["bad"] = true
	restored, err := stateTestSnapshot(ctx, store, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := service.DiscoveredToolsForSession(ctx, row.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].CanonicalName != "wisp/external" || discovered[0].Endpoint != item.Endpoint || discovered[0].TimeoutSeconds != 7 {
		t.Fatalf("restored=%+v", discovered)
	}
	if _, ok := discovered[0].Schema.Parameters["bad"]; ok {
		t.Fatal("output mutated persistence")
	}
	if _, ok := discovered[0].Schema.Parameters["properties"].(map[string]any)["bad"]; ok {
		t.Fatal("input mutated persistence")
	}
	discovered[0].Schema.Parameters["bad"] = true
	if _, ok := restored.DiscoveredTools[0].Schema.Parameters["bad"]; ok {
		t.Fatal("discovery view aliases state")
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	for _, want := range []string{`"unknown":9007199254740993`, `"workspace":"keep"`, `"title_source":"manual"`, `"TotalTokens":12`} {
		if !strings.Contains(latest.Metadata, want) {
			t.Fatalf("missing %s in %s", want, latest.Metadata)
		}
	}
	repeat, err := service.Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{stateTestTool("external")}})
	if err != nil || strings.Join(repeat.Injected, ",") != "external" || len(repeat.Existing) != 0 {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
	restored, err = stateTestSnapshot(ctx, store, row.ID)
	if err != nil || len(restored.DiscoveredTools) != 2 {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestStateCommitFailureKeepsAllFields(t *testing.T) {
	for _, metadata := range []string{`{"tool_tags":["old"],"unknown":5}`, `{"discovered_tools":"invalid","unknown":5}`, `{broken`} {
		t.Run(metadata, func(t *testing.T) {
			ctx := context.Background()
			store, row := stateTestStore(t, metadata)
			invalid := stateTestTool("new")
			invalid.Schema.Parameters["invalid"] = make(chan int)
			result, err := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools).Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{invalid}, Tags: []string{"new"}})
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
	_, err := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools).Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{stateTestTool("canceled")}})
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
	service := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
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
			_, err := service.Commit(ctx, row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{stateTestTool(fmt.Sprintf("t%d", i))}, Tags: []string{fmt.Sprintf("tag%d", i)}})
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			tokens := int64(i + 1)
			errs <- contexts.RecordTurn(ctx, row.ID, &llm.Usage{TotalTokens: &tokens})
		}(i)
		go func() {
			defer wg.Done()
			<-start
			snapshot, err := stateTestSnapshot(ctx, store, row.ID)
			if err == nil {
				for _, item := range snapshot.DiscoveredTools {
					item.Schema.Parameters["reader"] = true
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
	state, err := stateTestSnapshot(ctx, store, row.ID)
	if err != nil || len(state.DiscoveredTools) != count || len(state.ToolTags) != count {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	contextsState, err := contextmgr.DecodeState(latest.Metadata)
	if err != nil || contextsState.LastUsage == nil || contextsState.Compact == nil || contextsState.Compact.Summary != "seed" {
		t.Fatalf("context=%+v err=%v", contextsState, err)
	}
	if !strings.Contains(latest.Metadata, "9007199254740993") {
		t.Fatalf("lost committed state: %s", latest.Metadata)
	}
	if strings.Contains(latest.Metadata, "reader") {
		t.Fatal("reader mutated persisted schema")
	}
}
