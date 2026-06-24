package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/memory/resident"
	"elbot/internal/security"
	"elbot/internal/tool"
)

func raw(text string) json.RawMessage {
	return json.RawMessage(text)
}

func TestResidentMemoryToolsUseActorScope(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	coreTool := ResidentMemoryCoreTool{Store: store}
	normalTool := ResidentMemoryNormalTool{Store: store}
	readTool := ResidentMemoryReadTool{Store: store}
	ctx := security.WithActor(context.Background(), security.Actor{ID: "qqonebot:1", Platform: "qqonebot", PlatformUserID: "1", Role: security.RoleUser})

	if _, err := coreTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"content":"用户喜欢被称为娅娅。"}`)}); err != nil {
		t.Fatalf("write core: %v", err)
	}
	if _, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"write","content":"用户喜欢短回复。"}`)}); err != nil {
		t.Fatalf("write normal: %v", err)
	}
	result, err := readTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"section":"all"}`)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(result.Content, "用户喜欢被称为娅娅。") || !strings.Contains(result.Content, "用户喜欢短回复。") {
		t.Fatalf("content = %q", result.Content)
	}

	otherCtx := security.WithActor(context.Background(), security.Actor{ID: "qqonebot:2", Platform: "qqonebot", PlatformUserID: "2", Role: security.RoleUser})
	result, err = readTool.Call(otherCtx, tool.CallRequest{Arguments: raw(`{"section":"all"}`)})
	if err != nil {
		t.Fatalf("read other: %v", err)
	}
	if strings.Contains(result.Content, "用户喜欢短回复。") {
		t.Fatalf("other content = %q", result.Content)
	}
}

func TestResidentMemoryNormalAppendWriteDelete(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	normalTool := ResidentMemoryNormalTool{Store: store}
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	result, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append","content":"第一条"}`)})
	if err != nil {
		t.Fatalf("append empty: %v", err)
	}
	if result.Content != "已追加普通常驻记忆。" {
		t.Fatalf("append empty content = %q", result.Content)
	}
	memory, err := store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}))
	if err != nil || memory.Normal != "第一条" {
		t.Fatalf("memory = %#v err=%v", memory, err)
	}

	if _, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append","content":"第二条"}`)}); err != nil {
		t.Fatalf("append existing: %v", err)
	}
	memory, err = store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}))
	if err != nil || memory.Normal != "第一条 第二条" {
		t.Fatalf("memory = %#v err=%v", memory, err)
	}
	if _, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"delete"}`)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})); err != resident.ErrNotFound {
		t.Fatalf("expected empty after delete, err=%v", err)
	}
}

func TestResidentMemoryCoreAllowsEmptyString(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	coreTool := ResidentMemoryCoreTool{Store: store}
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	if _, err := coreTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"content":"核心"}`)}); err != nil {
		t.Fatalf("write core: %v", err)
	}
	if _, err := coreTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"content":""}`)}); err != nil {
		t.Fatalf("clear core: %v", err)
	}
	if _, err := store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})); err != resident.ErrNotFound {
		t.Fatalf("expected empty after clear, err=%v", err)
	}
	if _, err := coreTool.Call(ctx, tool.CallRequest{Arguments: raw(`{}`)}); err == nil {
		t.Fatalf("expected missing content error")
	}
}

func TestResidentMemoryToolRisksAndSchema(t *testing.T) {
	store := resident.NewStoreWithLimits("unused.toml", resident.Limits{Core: 200, Normal: 300})
	entry := NewResidentMemoryTool(store)
	if entry.Info().Risk != tool.RiskLow || len(entry.Info().DependsOn) != 3 {
		t.Fatalf("entry info = %#v", entry.Info())
	}
	readTool := ResidentMemoryReadTool{Store: store}
	normalTool := ResidentMemoryNormalTool{Store: store}
	coreTool := ResidentMemoryCoreTool{Store: store}
	if readTool.Info().Risk != tool.RiskLow {
		t.Fatalf("read risk not low")
	}
	if normalTool.Info().Risk != tool.RiskLow {
		t.Fatalf("normal risk not low")
	}
	if coreTool.Info().Risk != tool.RiskHigh {
		t.Fatalf("core risk not high")
	}
	schema := coreTool.Schema()
	props, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema.Function.Parameters["properties"])
	}
	for _, name := range []string{"platform", "actor_id", "scope_id", "id"} {
		if _, ok := props[name]; ok {
			t.Fatalf("schema exposes %s: %#v", name, props)
		}
	}
	if !strings.Contains(schema.Function.Description, "200 字数或单词") {
		t.Fatalf("core description = %q", schema.Function.Description)
	}
	if !strings.Contains(normalTool.Schema().Function.Description, "300 字数或单词") {
		t.Fatalf("normal description = %q", normalTool.Schema().Function.Description)
	}
}

func TestResidentMemoryValidation(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	readTool := ResidentMemoryReadTool{Store: store}
	normalTool := ResidentMemoryNormalTool{Store: store}
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	if _, err := readTool.Call(context.Background(), tool.CallRequest{Arguments: raw(`{"section":"all"}`)}); err == nil {
		t.Fatalf("expected missing actor error")
	}
	if _, err := readTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"section":"bad"}`)}); err == nil {
		t.Fatalf("expected invalid section error")
	}
	if _, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append"}`)}); err == nil {
		t.Fatalf("expected missing content error")
	}
	if _, err := normalTool.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"unknown"}`)}); err == nil {
		t.Fatalf("expected invalid action error")
	}
}
