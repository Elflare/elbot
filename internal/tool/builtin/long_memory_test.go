package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	
)

func writeSyncMemory(t *testing.T, store *longMemoryStore, name string, id int64, content string) string {
	t.Helper()
	record := longMemoryRecord{ID: id, Category: "sync", Title: name, Summary: "summary", Content: content, CreatedAt: "2026-01-01", UpdatedAt: "2026-01-01"}
	data, err := marshalLongMemoryFile(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.memoriesDir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLongMemorySyncFileIdentityChanges(t *testing.T) {
	for _, scenario := range []string{"rename", "change id", "swap ids", "duplicate later", "duplicate earlier", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			store := newLongMemoryStore(t.TempDir())
			t.Cleanup(func() { _ = store.close() })
			if err := store.ensureLocked(t.Context()); err != nil {
				t.Fatal(err)
			}
			path := writeSyncMemory(t, store, "b.md", 1, "oldtoken")
			if scenario == "swap ids" {
				writeSyncMemory(t, store, "c.md", 2, "othertoken")
			}
			if err := store.syncLocked(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			want := map[int64]string{1: "b.md"}
			invalidCount := 0
			switch scenario {
			case "rename":
				if err := os.Rename(path, filepath.Join(store.memoriesDir, "a.md")); err != nil {
					t.Fatal(err)
				}
				want[1] = "a.md"
			case "change id":
				writeSyncMemory(t, store, "b.md", 3, "newtoken")
				want = map[int64]string{3: "b.md"}
			case "swap ids":
				writeSyncMemory(t, store, "b.md", 2, "newtoken")
				writeSyncMemory(t, store, "c.md", 1, "othertoken")
				want = map[int64]string{1: "c.md", 2: "b.md"}
			case "duplicate later":
				writeSyncMemory(t, store, "c.md", 1, "newtoken")
				invalidCount = 1
			case "duplicate earlier":
				writeSyncMemory(t, store, "a.md", 1, "newtoken")
				want[1] = "a.md"
				invalidCount = 1
			case "invalid":
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
				want = map[int64]string{}
				invalidCount = 1
			}
			for range 2 {
				if err := store.syncLocked(t.Context(), true); err != nil {
					t.Fatal(err)
				}
				for id := int64(1); id <= 3; id++ {
					record, err := store.recordByIDLocked(t.Context(), id)
					name, exists := want[id]
					if !exists {
						if !errors.Is(err, sql.ErrNoRows) {
							t.Fatalf("unexpected id %d: %#v, %v", id, record, err)
						}
					} else if err != nil || filepath.Base(record.FilePath) != name {
						t.Fatalf("id %d = %#v, %v; want %s", id, record, err, name)
					}
				}
				invalids, err := store.invalidFilesLocked(t.Context(), 10)
				if err != nil || len(invalids) != invalidCount {
					t.Fatalf("invalids = %#v, %v", invalids, err)
				}
			}
			if scenario == "invalid" || scenario == "change id" || scenario == "swap ids" || scenario == "duplicate earlier" {
				records, err := store.searchFTSLocked(t.Context(), "oldtoken", "", "any", 10)
				if err != nil || len(records) != 0 {
					t.Fatalf("stale search results = %#v, %v", records, err)
				}
			}
		})
	}
}

func TestLongMemorySyncInvalidRecoveryAndRestart(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	if err := store.ensureLocked(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := writeSyncMemory(t, store, "a.md", 1, "firsttoken")
	writeSyncMemory(t, store, "b.md", 1, "secondtoken")
	broken := filepath.Join(store.memoriesDir, "broken.md")
	if err := os.WriteFile(broken, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.syncLocked(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.categories(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := store.recordByIDLocked(t.Context(), 1)
	if err != nil || filepath.Base(record.FilePath) != "b.md" {
		t.Fatalf("promoted record = %#v, %v", record, err)
	}
	invalids, err := store.invalidFilesLocked(t.Context(), 10)
	if err != nil || len(invalids) != 1 || invalids[0].FilePath != broken {
		t.Fatalf("invalids after restart = %#v, %v", invalids, err)
	}
	writeSyncMemory(t, store, "broken.md", 2, "repairedtoken")
	if err := store.syncLocked(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if invalids, err := store.invalidFilesLocked(t.Context(), 10); err != nil || len(invalids) != 0 {
		t.Fatalf("invalids after repair = %#v, %v", invalids, err)
	}
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	if err := store.syncLocked(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.recordByIDLocked(t.Context(), 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted record: %v", err)
	}
}

func TestLongMemorySyncFailureCanRetry(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	if err := store.ensureLocked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.categories(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.memoriesDir); err != nil {
		t.Fatal(err)
	}
	if err := store.syncLocked(t.Context(), true); err == nil {
		t.Fatal("expected missing directory error")
	}
	if err := os.MkdirAll(store.memoriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeSyncMemory(t, store, "a.md", 1, "recovered")
	result, err := store.categories(t.Context())
	if err != nil || !strings.Contains(result, "sync（共1条记录）") {
		t.Fatalf("retry result = %q, %v", result, err)
	}
	// Reading within the cooldown uses the existing index; the next eligible read refreshes it.
	writeSyncMemory(t, store, "b.md", 2, "next")
	result, err = store.categories(t.Context())
	if err != nil || !strings.Contains(result, "sync（共1条记录）") {
		t.Fatalf("cooldown result = %q, %v", result, err)
	}
	store.lastSync = time.Now().Add(-longMemorySyncCooldown)
	result, err = store.categories(t.Context())
	if err != nil || !strings.Contains(result, "sync（共2条记录）") {
		t.Fatalf("refreshed result = %q, %v", result, err)
	}
}

func TestLongMemorySyncDuplicateOwnerBecomesInvalid(t *testing.T) {
	for _, scenario := range []string{"removed", "changed id", "broken syntax", "invalid metadata"} {
		t.Run(scenario, func(t *testing.T) {
			store := newLongMemoryStore(t.TempDir())
			t.Cleanup(func() { _ = store.close() })
			if err := store.ensureLocked(t.Context()); err != nil {
				t.Fatal(err)
			}
			first := writeSyncMemory(t, store, "a.md", 1, "firsttoken")
			writeSyncMemory(t, store, "b.md", 1, "secondtoken")
			if err := store.syncLocked(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "removed":
				if err := os.Remove(first); err != nil {
					t.Fatal(err)
				}
			case "changed id":
				writeSyncMemory(t, store, "a.md", 2, "changedtoken")
			case "broken syntax":
				if err := os.WriteFile(first, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid metadata":
				writeSyncMemory(t, store, "a.md", 0, "invalidtoken")
			}
			for range 2 {
				if err := store.syncLocked(t.Context(), true); err != nil {
					t.Fatal(err)
				}
				record, err := store.recordByIDLocked(t.Context(), 1)
				if err != nil || filepath.Base(record.FilePath) != "b.md" {
					t.Fatalf("new owner = %#v, %v", record, err)
				}
				records, err := store.searchFTSLocked(t.Context(), "firsttoken", "", "any", 10)
				if err != nil || len(records) != 0 {
					t.Fatalf("old owner remains searchable: %#v, %v", records, err)
				}
			}
			if scenario == "broken syntax" || scenario == "invalid metadata" {
				if err := os.Remove(first); err != nil {
					t.Fatal(err)
				}
				if err := store.syncLocked(t.Context(), true); err != nil {
					t.Fatal(err)
				}
				if invalids, err := store.invalidFilesLocked(t.Context(), 10); err != nil || len(invalids) != 0 {
					t.Fatalf("deleted invalid file still reported: %#v, %v", invalids, err)
				}
			}
		})
	}
}

func TestPreparedLongMemoryUpdatePreservesConfirmation(t *testing.T) {
	for _, scenario := range []string{"unchanged", "external edit", "newer update", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			store := newLongMemoryStore(t.TempDir())
			t.Cleanup(func() { _ = store.close() })
			writer := LongMemoryWriteTool{store: store}
			record, err := store.save(t.Context(), longMemorySaveArgs{Category: "测试", Title: "测试记忆", Summary: "摘要", Keywords: "alpha", Content: "原文"})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := writer.PrepareCall(t.Context(), toolshare.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"content":"已确认正文"}`)})
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(record.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			want := string(original)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "external edit":
				want = strings.ReplaceAll(want, "原文", "外部")
				if err := os.WriteFile(record.FilePath, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			case "newer update":
				if _, _, err := writer.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"content":"其他调用写入"}`)}); err != nil {
					t.Fatal(err)
				}
				current, err := os.ReadFile(record.FilePath)
				if err != nil {
					t.Fatal(err)
				}
				want = string(current)
			case "canceled":
				cancel()
			}
			_, _, err = prepared.Call(ctx)
			if (err == nil) != (scenario == "unchanged") {
				t.Fatalf("commit = %v", err)
			}
			current, err := os.ReadFile(record.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unchanged" {
				if !strings.Contains(string(current), "已确认正文") {
					t.Fatalf("content = %s", current)
				}
			} else if string(current) != want {
				t.Fatalf("refused update changed file: %s", current)
			}
		})
	}
}

func TestLongMemoryToolsHaveAgentTag(t *testing.T) {
	for _, memoryTool := range NewLongMemoryTools(t.TempDir()) {
		if got := strings.Join(memoryTool.Info().Tags, ","); got != "agent" {
			t.Fatalf("%s tags = %q", memoryTool.Name(), got)
		}
	}
}

func TestLongMemoryToolSetIsCompact(t *testing.T) {
	tools := NewLongMemoryTools(t.TempDir())
	if len(tools) != 3 {
		t.Fatalf("tool count = %d", len(tools))
	}
	got := []string{}
	for _, memoryTool := range tools {
		got = append(got, memoryTool.Name())
	}
	if strings.Join(got, ",") != "long_memory,long_memory_search,long_memory_write" {
		t.Fatalf("tools = %q", strings.Join(got, ","))
	}
	if deps := strings.Join(tools[0].Info().DependsOn, ","); deps != "long_memory_search,long_memory_write" {
		t.Fatalf("dependencies = %q", deps)
	}
}

func TestLongMemorySearchAndWriteTools(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	var searchTool *LongMemorySearchTool
	var writeTool *LongMemoryWriteTool
	for _, memoryTool := range []toolshare.Tool{LongMemoryTool{store: store}, LongMemorySearchTool{store: store}, LongMemoryWriteTool{store: store}} {
		switch typed := memoryTool.(type) {
		case LongMemorySearchTool:
			searchTool = &typed
		case LongMemoryWriteTool:
			writeTool = &typed
		}
	}
	if searchTool == nil || writeTool == nil {
		t.Fatal("missing compact long memory tools")
	}

	result, _, err := searchTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "长期记忆库为空") {
		t.Fatalf("empty categories = %q", result.Content)
	}

	_, _, err = writeTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha beta","content":"完整内容 alpha"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err = searchTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{"keywords":"alpha","brief_only":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "测试记忆") {
		t.Fatalf("search result = %q", result.Content)
	}

	_, _, err = writeTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"summary":"新摘要"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err = searchTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{"keywords":"alpha","brief_only":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "新摘要") {
		t.Fatalf("updated search result = %q", result.Content)
	}

	_, _, err = writeTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{"operation":"delete","id":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err = searchTool.Call(context.Background(), toolshare.CallRequest{Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "长期记忆库为空") {
		t.Fatalf("categories after delete = %q", result.Content)
	}
}

func TestLongMemoryUpdateContentEditsAndRiskDetail(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	searchTool := LongMemorySearchTool{store: store}
	ctx := context.Background()

	_, _, err := writeTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha beta","content":"第一行\n第二行\n第三行"}`)})
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"operation":"update","id":1,"summary":"新摘要","content_edits":[{"operation":"replace","line":2,"new_text":"第二行已更新\n"}]}`)
	prepared, err := writeTool.PrepareCall(ctx, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	detail := prepared.RiskDetail()
	for _, want := range []string{"操作：更新长期记忆", "字段变化：", "摘要：摘要 -> 新摘要", "正文编辑：content_edits", "-第二行", "+第二行已更新"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("risk detail missing %q:\n%s", want, detail)
		}
	}
	if _, _, err := prepared.Call(ctx); err != nil {
		t.Fatal(err)
	}
	result, _, err := searchTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"keywords":"已更新","brief_only":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"新摘要", "2 | 第二行已更新", "正文行号从 1 开始"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("search result missing %q:\n%s", want, result.Content)
		}
	}
}

func TestLongMemoryUpdateContentReplacesWholeBody(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	searchTool := LongMemorySearchTool{store: store}
	ctx := context.Background()

	_, _, err := writeTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"旧正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"content":"新正文\n第二行"}`)}); err != nil {
		t.Fatal(err)
	}
	result, _, err := searchTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"keywords":"新正文","brief_only":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 | 新正文", "2 | 第二行"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("search result missing %q:\n%s", want, result.Content)
		}
	}
}

func TestLongMemoryUpdateRejectsNoChanges(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	ctx := context.Background()

	_, _, err := writeTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = writeTool.PrepareCall(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"summary":"摘要"}`)})
	if err == nil || !strings.Contains(err.Error(), "edit produced no changes") {
		t.Fatalf("expected no-op error, got %v", err)
	}
}

func TestLongMemoryUpdateRejectsContentAndContentEditsTogether(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	ctx := context.Background()

	_, _, err := writeTool.Call(ctx, toolshare.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"operation":"update","id":1,"content":"整段正文","content_edits":[{"operation":"insert","line":2,"new_text":"追加"}]}`)
	_, err = writeTool.PrepareCall(ctx, toolshare.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "content and content_edits cannot be used together") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}
