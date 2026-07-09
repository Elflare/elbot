package resident

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/session"
)

func TestStoreWriteReadAndDeleteNormal(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:1"}

	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read empty error = %v", err)
	}
	if err := store.WriteCore(context.Background(), scope, "喜欢被称为娅娅"); err != nil {
		t.Fatalf("WriteCore: %v", err)
	}
	if err := store.WriteNormal(context.Background(), scope, "喜欢简短回答"); err != nil {
		t.Fatalf("WriteNormal: %v", err)
	}
	memory, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if memory.Core != "喜欢被称为娅娅" || memory.Normal != "喜欢简短回答" {
		t.Fatalf("memory = %#v", memory)
	}
	if err := store.DeleteNormal(context.Background(), scope); err != nil {
		t.Fatalf("DeleteNormal: %v", err)
	}
	memory, err = store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read after delete normal: %v", err)
	}
	if memory.Core != "喜欢被称为娅娅" || memory.Normal != "" {
		t.Fatalf("memory after delete normal = %#v", memory)
	}
	if err := store.WriteCore(context.Background(), scope, ""); err != nil {
		t.Fatalf("clear core: %v", err)
	}
	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read cleared error = %v", err)
	}
}

func TestStoreIsolatesPlatformAndActor(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	qq := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:1"}
	cli := session.Scope{Platform: "cli", ActorID: "cli:1"}
	other := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:2"}
	if err := store.WriteNormal(context.Background(), qq, "qq memory"); err != nil {
		t.Fatalf("Write qq: %v", err)
	}
	if err := store.WriteNormal(context.Background(), cli, "cli memory"); err != nil {
		t.Fatalf("Write cli: %v", err)
	}
	memory, err := store.Read(context.Background(), qq)
	if err != nil || memory.Normal != "qq memory" {
		t.Fatalf("qq memory = %#v, %v", memory, err)
	}
	memory, err = store.Read(context.Background(), cli)
	if err != nil || memory.Normal != "cli memory" {
		t.Fatalf("cli memory = %#v, %v", memory, err)
	}
	if _, err := store.Read(context.Background(), other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other read error = %v", err)
	}
}

func TestStoreMaxUnits(t *testing.T) {
	store := NewStoreWithLimits(filepath.Join(t.TempDir(), "memories.toml"), Limits{Core: 3, Normal: 2})
	if err := store.WriteCore(context.Background(), session.Scope{Platform: "cli", ActorID: "cli:local"}, "四个汉字"); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("WriteCore long error = %v", err)
	}
	if err := store.WriteNormal(context.Background(), session.Scope{Platform: "cli", ActorID: "cli:local"}, "one two three"); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("WriteNormal long error = %v", err)
	}
	if CountUnits("用户 likes short replies") != 5 {
		t.Fatalf("CountUnits mixed = %d", CountUnits("用户 likes short replies"))
	}
}

func TestStoreNormalWriteIgnoresExistingOverLimitCore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	data := []byte("[[resident_memories]]\nplatform = \"cli\"\nactor_id = \"cli:local\"\ncore = \"四个汉字\"\nnormal = \"旧\"\ncreated_at = \"t1\"\nupdated_at = \"t1\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	store := NewStoreWithLimits(path, Limits{Core: 3, Normal: 5})
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}
	if err := store.WriteNormal(context.Background(), scope, "新 normal"); err != nil {
		t.Fatalf("WriteNormal with over-limit core: %v", err)
	}
	if err := store.AppendNormal(context.Background(), scope, "追加"); err != nil {
		t.Fatalf("AppendNormal with over-limit core: %v", err)
	}
	memory, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if memory.Core != "四个汉字" || memory.Normal != "新 normal 追加" {
		t.Fatalf("memory = %#v", memory)
	}
}

func TestStoreReloadsExternalFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	store := NewStore(path)
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}
	if err := store.WriteNormal(context.Background(), scope, "旧记忆"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if memory, err := store.Read(context.Background(), scope); err != nil || memory.Normal != "旧记忆" {
		t.Fatalf("Read cached = %#v, %v", memory, err)
	}

	data := []byte("[[resident_memories]]\nplatform = \"cli\"\nactor_id = \"cli:local\"\ncore = \"核心\"\nnormal = \"外部更新\"\ncreated_at = \"t1\"\nupdated_at = \"t2\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	changedAt := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, changedAt, changedAt); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	memory, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read external update: %v", err)
	}
	if memory.Core != "核心" || memory.Normal != "外部更新" {
		t.Fatalf("memory after external update = %#v", memory)
	}
}

func TestStoreWriteAndDeleteRefreshCache(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}
	if err := store.WriteNormal(context.Background(), scope, "第一版"); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := store.WriteNormal(context.Background(), scope, "第二版"); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	memory, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read after second write: %v", err)
	}
	if memory.Normal != "第二版" {
		t.Fatalf("memory after second write = %#v", memory)
	}
	if err := store.DeleteNormal(context.Background(), scope); err != nil {
		t.Fatalf("DeleteNormal: %v", err)
	}
	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read deleted cached error = %v", err)
	}
}
