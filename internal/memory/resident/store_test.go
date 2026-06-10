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

func TestStoreWriteReadDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:1"}

	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read empty error = %v", err)
	}
	if err := store.Write(context.Background(), scope, "喜欢简短回答"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	content, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content != "喜欢简短回答" {
		t.Fatalf("content = %q", content)
	}
	if err := store.Write(context.Background(), scope, "正在开发 ElBot"); err != nil {
		t.Fatalf("Write update: %v", err)
	}
	content, err = store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read updated: %v", err)
	}
	if content != "正在开发 ElBot" {
		t.Fatalf("updated content = %q", content)
	}
	if err := store.Delete(context.Background(), scope); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read deleted error = %v", err)
	}
}

func TestStoreIsolatesPlatformAndActor(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	qq := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:1"}
	cli := session.Scope{Platform: "cli", ActorID: "cli:1"}
	other := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:2"}
	if err := store.Write(context.Background(), qq, "qq memory"); err != nil {
		t.Fatalf("Write qq: %v", err)
	}
	if err := store.Write(context.Background(), cli, "cli memory"); err != nil {
		t.Fatalf("Write cli: %v", err)
	}
	content, err := store.Read(context.Background(), qq)
	if err != nil || content != "qq memory" {
		t.Fatalf("qq content = %q, %v", content, err)
	}
	content, err = store.Read(context.Background(), cli)
	if err != nil || content != "cli memory" {
		t.Fatalf("cli content = %q, %v", content, err)
	}
	if _, err := store.Read(context.Background(), other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other read error = %v", err)
	}
}

func TestStoreMaxChars(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	store.MaxChars = 3
	err := store.Write(context.Background(), session.Scope{Platform: "cli", ActorID: "cli:local"}, "abcd")
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("Write long error = %v", err)
	}
}

func TestStoreReloadsExternalFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	store := NewStore(path)
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}
	if err := store.Write(context.Background(), scope, "旧记忆"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if content, err := store.Read(context.Background(), scope); err != nil || content != "旧记忆" {
		t.Fatalf("Read cached = %q, %v", content, err)
	}

	data := []byte("[[resident_memories]]\nplatform = \"cli\"\nactor_id = \"cli:local\"\ncontent = \"外部更新\"\ncreated_at = \"t1\"\nupdated_at = \"t2\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	changedAt := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, changedAt, changedAt); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	content, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read external update: %v", err)
	}
	if content != "外部更新" {
		t.Fatalf("content after external update = %q", content)
	}
}

func TestStoreWriteAndDeleteRefreshCache(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}
	if err := store.Write(context.Background(), scope, "第一版"); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := store.Write(context.Background(), scope, "第二版"); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	content, err := store.Read(context.Background(), scope)
	if err != nil {
		t.Fatalf("Read after second write: %v", err)
	}
	if content != "第二版" {
		t.Fatalf("content after second write = %q", content)
	}
	if err := store.Delete(context.Background(), scope); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read deleted cached error = %v", err)
	}
}
