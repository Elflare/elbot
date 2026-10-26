package state

import (
	toolregistry "elbot/internal/tool/registry"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"elbot/internal/events"
	toolshare "elbot/internal/tool/share"
)

func TestDiscoveredToolsMergeOrderAndReplacement(t *testing.T) {
	alpha, beta := stateTestTool("alpha"), stateTestTool("beta")
	updated := alpha
	updated.Description = "updated"
	batch := normalizeDiscoveredTools([]toolshare.DiscoveredTool{beta, alpha, beta})
	incremental := mergeDiscoveredTools(mergeDiscoveredTools(nil, []toolshare.DiscoveredTool{beta}), []toolshare.DiscoveredTool{alpha})
	reverse := mergeDiscoveredTools(mergeDiscoveredTools(nil, []toolshare.DiscoveredTool{alpha}), []toolshare.DiscoveredTool{beta})
	if !reflect.DeepEqual(batch, incremental) || !reflect.DeepEqual(batch, reverse) {
		t.Fatalf("unstable order: batch=%+v incremental=%+v reverse=%+v", batch, incremental, reverse)
	}
	merged := mergeDiscoveredTools(batch, []toolshare.DiscoveredTool{updated})
	if len(merged) != 2 || merged[0].Name != "alpha" || merged[0].Description != "updated" || batch[0].Description != "" {
		t.Fatalf("replacement=%+v original=%+v", merged, batch)
	}
}

func TestStatePersistsCompleteDeclarationsByCanonicalName(t *testing.T) {
	store, row := stateTestStore(t, "")
	service := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	left, right := stateTestTool("remote"), stateTestTool("remote")
	left.Source, right.Source = toolshare.SourceKindELwisp, toolshare.SourceKindELwisp
	left.CanonicalName, right.CanonicalName = "left_remote", "right_remote"
	left.Schema.Name, right.Schema.Name = left.CanonicalName, right.CanonicalName
	left.Endpoint, right.Endpoint = "https://left.invalid", "https://right.invalid"
	left.EventKey, right.EventKey = "left-event", "right-event"
	first, _, err := service.Commit(t.Context(), row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{right, left}})
	if err != nil || !slices.Equal(first.Injected, []string{"left_remote", "right_remote"}) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	left.Endpoint = "https://updated.invalid"
	next, _, err := service.Commit(t.Context(), row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{left}})
	if err != nil || len(next.Injected) != 0 || !slices.Equal(next.Existing, []string{"left_remote"}) {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(next.Metadata), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 {
		t.Fatalf("unexpected state fields: %s", next.Metadata)
	}
	var stored []toolshare.DiscoveredTool
	if err := json.Unmarshal(fields["discovered_tools"], &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Endpoint != left.Endpoint || stored[1].Endpoint != right.Endpoint || stored[0].EventKey != left.EventKey {
		t.Fatalf("stored=%+v", stored)
	}
	restored, logResult, err := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools).DiscoveredToolsForSession(t.Context(), row.ID, false)
	if err != nil || logResult.ResultStatus != events.ResultSucceeded || !reflect.DeepEqual(restored, stored) {
		t.Fatalf("restored=%+v log=%+v err=%v", restored, logResult, err)
	}
}

func TestStateReadUsesPersistedDeclarationsAndBackgroundRules(t *testing.T) {
	store, row := stateTestStore(t, "")
	service := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	regular, front, workspace := stateTestTool("unregistered"), stateTestTool("front"), stateTestTool("workspace")
	regular.Schema.Description = "persisted definition"
	front.ForegroundOnly = true
	if _, _, err := service.Commit(t.Context(), row.ID, toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{regular, front, workspace}}); err != nil {
		t.Fatal(err)
	}
	all, _, err := service.DiscoveredToolsForSession(t.Context(), row.ID, false)
	if err != nil || len(all) != 3 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	background, _, err := service.DiscoveredToolsForSession(t.Context(), row.ID, true)
	if err != nil || len(background) != 1 || background[0].Name != regular.Name || background[0].Schema.Description != "persisted definition" {
		t.Fatalf("background=%+v err=%v", background, err)
	}
	background[0].Schema.Parameters["changed"] = true
	again, _, err := service.DiscoveredToolsForSession(t.Context(), row.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := again[0].Schema.Parameters["changed"]; exists {
		t.Fatal("reader changed persisted declaration")
	}
}

func TestPrepareUpdateOwnsOnlyToolFieldsAndMatchesCommit(t *testing.T) {
	const raw = `{"unknown":9007199254740993,"elyph_rules_shown":true,"tool_tags":["old"]}`
	store, row := stateTestStore(t, raw)
	service := NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	item := stateTestTool("new")
	update := toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{item}, Tags: []string{"new"}}
	patch, err := PrepareUpdate(raw, update)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 2 || patch["discovered_tools"] == nil || patch["tool_tags"] == nil {
		t.Fatalf("patch contains fields outside tool state: %v", patch)
	}
	unchanged, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || unchanged.Metadata != raw {
		t.Fatalf("prepare wrote storage: row=%+v err=%v", unchanged, err)
	}
	committed, _, err := service.Commit(t.Context(), row.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(committed.Metadata), &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range patch {
		if !reflect.DeepEqual(value, fields[key]) {
			t.Fatalf("prepare and commit differ for %s: %s / %s", key, value, fields[key])
		}
	}
	item.Schema.Parameters["changed"] = true
	encoded, err := patch.Encode()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := DecodeState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prepared.DiscoveredTools[0].Schema.Parameters["changed"]; ok {
		t.Fatal("input mutated prepared patch")
	}
	if !committed.State.ElyphRulesShown || string(fields["unknown"]) != "9007199254740993" {
		t.Fatalf("commit changed another owner's fields: %s", committed.Metadata)
	}
}

func TestPrepareUpdateFailureReturnsNoPartialPatch(t *testing.T) {
	item := stateTestTool("invalid")
	item.Schema.Parameters["unsupported"] = make(chan int)
	for _, tc := range []struct {
		raw    string
		update toolshare.StateUpdate
	}{
		{raw: "{broken"},
		{raw: `{"discovered_tools":"invalid"}`},
		{raw: `{"tool_tags":["old"]}`, update: toolshare.StateUpdate{Tools: []toolshare.DiscoveredTool{item}, Tags: []string{"new"}}},
	} {
		patch, err := PrepareUpdate(tc.raw, tc.update)
		if err == nil || patch != nil {
			t.Fatalf("partial patch=%v err=%v", patch, err)
		}
	}
	patch, err := PrepareUpdate(`{"unknown":1}`, toolshare.StateUpdate{})
	if err != nil || len(patch) != 0 {
		t.Fatalf("empty update=%v err=%v", patch, err)
	}
}
