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

func TestResidentMemoryToolUsesActorScope(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	tm := NewResidentMemoryTool(store)
	ctx := security.WithActor(context.Background(), security.Actor{ID: "qqonebot:1", Platform: "qqonebot", PlatformUserID: "1", Role: security.RoleUser})

	if _, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"write","content":"用户喜欢短回复。"}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"read"}`)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.Content != "用户喜欢短回复。" {
		t.Fatalf("content = %q", result.Content)
	}

	otherCtx := security.WithActor(context.Background(), security.Actor{ID: "qqonebot:2", Platform: "qqonebot", PlatformUserID: "2", Role: security.RoleUser})
	result, err = tm.Call(otherCtx, tool.CallRequest{Arguments: raw(`{"action":"read"}`)})
	if err != nil {
		t.Fatalf("read other: %v", err)
	}
	if result.Content != "常驻记忆为空。" {
		t.Fatalf("other content = %q", result.Content)
	}
}

func TestResidentMemoryToolAppend(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	tm := NewResidentMemoryTool(store)
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	result, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append","content":"第一条"}`)})
	if err != nil {
		t.Fatalf("append empty: %v", err)
	}
	if result.Content != "已更新常驻记忆。" {
		t.Fatalf("append empty content = %q", result.Content)
	}
	stored, err := store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}))
	if err != nil || stored != "第一条" {
		t.Fatalf("stored = %q err=%v", stored, err)
	}

	result, err = tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append","content":"第二条"}`)})
	if err != nil {
		t.Fatalf("append existing: %v", err)
	}
	if result.Content != "已追加常驻记忆。" {
		t.Fatalf("append existing content = %q", result.Content)
	}
	stored, err = store.Read(ctx, resident.ActorScope(security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}))
	if err != nil || stored != "第一条\n第二条" {
		t.Fatalf("stored = %q err=%v", stored, err)
	}
}

func TestResidentMemoryToolAppendTooLongDoesNotWrite(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	store.MaxChars = 8
	tm := NewResidentMemoryTool(store)
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	if _, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"write","content":"1234"}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"append","content":"5678"}`)})
	if err != nil {
		t.Fatalf("append too long: %v", err)
	}
	if !strings.Contains(result.Content, "先 read 当前记忆") {
		t.Fatalf("too long content=%q", result.Content)
	}
	readResult, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"read"}`)})
	if err != nil {
		t.Fatalf("read after too long: %v", err)
	}
	if readResult.Content != "1234" {
		t.Fatalf("memory was changed: %q", readResult.Content)
	}
}

func TestResidentMemoryToolDeleteAndValidation(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	tm := NewResidentMemoryTool(store)
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})

	if _, err := tm.Call(context.Background(), tool.CallRequest{Arguments: raw(`{"action":"read"}`)}); err == nil {
		t.Fatalf("expected missing actor error")
	}
	if _, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"write"}`)}); err == nil {
		t.Fatalf("expected missing content error")
	}
	if _, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"unknown"}`)}); err == nil {
		t.Fatalf("expected invalid action error")
	}
	if _, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"write","content":"记住这个。"}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := tm.Call(ctx, tool.CallRequest{Arguments: raw(`{"action":"delete"}`)})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Content != "已删除常驻记忆。" {
		t.Fatalf("delete content = %q", result.Content)
	}
}

func TestResidentMemoryToolSchemaDoesNotExposeIdentity(t *testing.T) {
	schema := NewResidentMemoryTool(resident.NewStore("unused.toml")).Schema()
	props, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema.Function.Parameters["properties"])
	}
	for _, name := range []string{"platform", "actor_id", "scope_id", "id"} {
		if _, ok := props[name]; ok {
			t.Fatalf("schema exposes %s: %#v", name, props)
		}
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing action: %#v", props)
	}
	if description, _ := action["description"].(string); !strings.Contains(description, "append") {
		t.Fatalf("action description does not mention append: %q", description)
	}
	if _, ok := props["content"]; !ok {
		t.Fatalf("schema missing content: %#v", props)
	}
}
