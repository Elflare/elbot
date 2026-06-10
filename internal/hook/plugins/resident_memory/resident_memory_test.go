package resident_memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/session"
)

func TestModuleInjectsSavedResidentMemory(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	if err := store.Write(context.Background(), residentScope("qqonebot", "qqonebot:1"), "用户喜欢简短回答。"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	event := hook.Event{
		Point:    hook.PointLLMTurnPrepared,
		Platform: hook.PlatformContext{Name: "qqonebot"},
		Actor:    hook.ActorContext{ID: "qqonebot:1", DisplayName: "小娅(qq:1)"},
		LLM:      hook.LLMPayload{Messages: []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments("SOUL")}, {Role: llm.RoleUser, Segments: llm.TextSegments("hi")}}},
	}
	updated, err := NewModule(Options{Store: store}).inject(context.Background(), event)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := llm.SegmentsContentText(updated.LLM.Messages[0].Segments); !strings.Contains(got, "SOUL") || !strings.Contains(got, "用户喜欢简短回答。") {
		t.Fatalf("system content = %q", got)
	}
}

func TestModuleInjectsDefaultDisplayNameWhenMemoryIsEmpty(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	event := hook.Event{
		Point:    hook.PointLLMTurnPrepared,
		Platform: hook.PlatformContext{Name: "qqonebot"},
		Actor:    hook.ActorContext{ID: "qqonebot:1", DisplayName: "群名片(qq:1)"},
		LLM:      hook.LLMPayload{Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("hi")}}},
	}
	updated, err := NewModule(Options{Store: store}).inject(context.Background(), event)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if len(updated.LLM.Messages) != 2 || updated.LLM.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("messages = %#v", updated.LLM.Messages)
	}
	if llm.SegmentsContentText(updated.LLM.Messages[0].Segments) != "用户名字：群名片(qq:1)。" {
		t.Fatalf("default memory = %q", llm.SegmentsContentText(updated.LLM.Messages[0].Segments))
	}
	if _, err := store.Read(context.Background(), residentScope("qqonebot", "qqonebot:1")); !errors.Is(err, resident.ErrNotFound) {
		t.Fatalf("default memory was persisted: %v", err)
	}
}

func TestModuleUsesCLIAdminDefaultName(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	event := hook.Event{
		Point:    hook.PointLLMTurnPrepared,
		Platform: hook.PlatformContext{Name: "cli"},
		Actor:    hook.ActorContext{ID: "cli:local"},
		LLM:      hook.LLMPayload{Messages: []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments("SOUL")}}},
	}
	updated, err := NewModule(Options{Store: store}).inject(context.Background(), event)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := llm.SegmentsContentText(updated.LLM.Messages[0].Segments); !strings.Contains(got, "用户名字：管理员。") {
		t.Fatalf("system content = %q", got)
	}
}

func residentScope(platform, actorID string) session.Scope {
	return session.Scope{Platform: platform, ActorID: actorID}
}
