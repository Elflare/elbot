package tool

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/memory/resident"
	"elbot/internal/tool/builtin"
)

func raw(text string) json.RawMessage {
	return json.RawMessage(text)
}

func TestResidentMemoryToolsUseActorScope(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	coreTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[3]
	normalTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[2]
	readTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[1]
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "qqonebot:1", Platform: "qqonebot", PlatformUserID: "1", Role: contextinfo.RoleUser})

	if _, _, err := callTool(ctx, coreTool, toolshare.CallRequest{Arguments: raw(`{"content":"用户喜欢被称为娅娅。"}`)}); err != nil {
		t.Fatalf("write core: %v", err)
	}
	if _, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"write","content":"用户喜欢短回复。"}`)}); err != nil {
		t.Fatalf("write normal: %v", err)
	}
	result, logContent, err := callTool(ctx, readTool, toolshare.CallRequest{Arguments: raw(`{"section":"all"}`)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(result.Content, "用户喜欢被称为娅娅。") || !strings.Contains(result.Content, "用户喜欢短回复。") {
		t.Fatalf("content = %q", result.Content)
	}
	if !strings.Contains(logContent, "section=all") || strings.Contains(logContent, "用户喜欢") || len([]rune(logContent)) > 1024 {
		t.Fatalf("memory read should log scope and size: %q", logContent)
	}

	otherCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "qqonebot:2", Platform: "qqonebot", PlatformUserID: "2", Role: contextinfo.RoleUser})
	result, _, err = callTool(otherCtx, readTool, toolshare.CallRequest{Arguments: raw(`{"section":"all"}`)})
	if err != nil {
		t.Fatalf("read other: %v", err)
	}
	if strings.Contains(result.Content, "用户喜欢短回复。") {
		t.Fatalf("other content = %q", result.Content)
	}
}

func TestResidentMemoryNormalAppendWriteDelete(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	normalTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[2]
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})

	result, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"append","content":"第一条"}`)})
	if err != nil {
		t.Fatalf("append empty: %v", err)
	}
	if result.Content != "已追加普通常驻记忆。" {
		t.Fatalf("append empty content = %q", result.Content)
	}
	memory, err := store.Read(ctx, resident.ActorScope(contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}))
	if err != nil || memory.Normal != "第一条" {
		t.Fatalf("memory = %#v err=%v", memory, err)
	}

	if _, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"append","content":"第二条"}`)}); err != nil {
		t.Fatalf("append existing: %v", err)
	}
	memory, err = store.Read(ctx, resident.ActorScope(contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}))
	if err != nil || memory.Normal != "第一条 第二条" {
		t.Fatalf("memory = %#v err=%v", memory, err)
	}
	if _, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"delete"}`)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Read(ctx, resident.ActorScope(contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})); err != resident.ErrNotFound {
		t.Fatalf("expected empty after delete, err=%v", err)
	}
}

func TestResidentMemoryCoreAllowsEmptyString(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	coreTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[3]
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})

	if _, _, err := callTool(ctx, coreTool, toolshare.CallRequest{Arguments: raw(`{"content":"核心"}`)}); err != nil {
		t.Fatalf("write core: %v", err)
	}
	if _, _, err := callTool(ctx, coreTool, toolshare.CallRequest{Arguments: raw(`{"content":""}`)}); err != nil {
		t.Fatalf("clear core: %v", err)
	}
	if _, err := store.Read(ctx, resident.ActorScope(contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})); err != resident.ErrNotFound {
		t.Fatalf("expected empty after clear, err=%v", err)
	}
	if _, _, err := callTool(ctx, coreTool, toolshare.CallRequest{Arguments: raw(`{}`)}); err == nil {
		t.Fatalf("expected missing content error")
	}
}

func TestResidentMemoryToolRisksAndSchema(t *testing.T) {
	store := resident.NewStoreWithLimits("unused.toml", resident.Limits{Core: 200, Normal: 300})
	entry := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[0]
	if entry.Info().Risk != toolshare.RiskLow || len(entry.Info().DependsOn) != 3 {
		t.Fatalf("entry info = %#v", entry.Info())
	}
	readTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[1]
	normalTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[2]
	coreTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[3]
	if readTool.Info().Risk != toolshare.RiskLow {
		t.Fatalf("read risk not low")
	}
	if normalTool.Info().Risk != toolshare.RiskLow {
		t.Fatalf("normal risk not low")
	}
	if coreTool.Info().Risk != toolshare.RiskHigh {
		t.Fatalf("core risk not high")
	}
	schema := coreTool.Schema()
	if !strings.Contains(schema.Description, "200 字数或单词") {
		t.Fatalf("core description = %q", schema.Description)
	}
	for name, description := range map[string]string{
		"core":   schema.Description,
		"normal": normalTool.Schema().Description,
	} {
		for _, want := range []string{"第三人称", "用户", "assistant", "不记录对模型的指令"} {
			if !strings.Contains(description, want) {
				t.Fatalf("%s description missing %q: %q", name, want, description)
			}
		}
	}
	if !strings.Contains(normalTool.Schema().Description, "300 字数或单词") {
		t.Fatalf("normal description = %q", normalTool.Schema().Description)
	}
}

func TestResidentMemoryValidation(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	readTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[1]
	normalTool := builtin.NewResidentMemoryTools(residentMemoryBridge{store: store})[2]
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})

	if _, _, err := callTool(context.Background(), readTool, toolshare.CallRequest{Arguments: raw(`{"section":"all"}`)}); err == nil {
		t.Fatalf("expected missing actor error")
	}
	if _, _, err := callTool(ctx, readTool, toolshare.CallRequest{Arguments: raw(`{"section":"bad"}`)}); err == nil {
		t.Fatalf("expected invalid section error")
	}
	if _, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"append"}`)}); err == nil {
		t.Fatalf("expected missing content error")
	}
	if _, _, err := callTool(ctx, normalTool, toolshare.CallRequest{Arguments: raw(`{"action":"unknown"}`)}); err == nil {
		t.Fatalf("expected invalid action error")
	}
}
