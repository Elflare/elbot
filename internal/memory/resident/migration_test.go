package resident

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
)

func TestResidentMigrationPreservesNativeIDAndWritesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	old := `[[resident_memories]]
platform = "telegram"
actor_id = "telegram:user:42"
core = "keep memory"
created_at = "created"
updated_at = "updated"
`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "user:42"}}
	store := NewStore(path)
	got, err := store.Read(t.Context(), scope)
	if err != nil || got.Core != "keep memory" {
		t.Fatalf("memory=%+v err=%v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "actor_id") || !strings.Contains(string(data), "version = 2") || !strings.Contains(string(data), "user:42") {
		t.Fatalf("migrated file: %s", data)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewStore(path).Read(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("reopening rewrote migrated file")
	}
}

func TestResidentMigrationRejectsAmbiguityAndDuplicateOwners(t *testing.T) {
	for _, old := range []string{
		"[[resident_memories]]\nplatform='telegram'\nactor_id='untyped'\ncore='keep'\n",
		strings.Repeat("[[resident_memories]]\nplatform='telegram'\nactor_id='telegram:1'\ncore='keep'\n", 2),
	} {
		path := filepath.Join(t.TempDir(), "memories.toml")
		if err := os.WriteFile(path, []byte(old), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(path).Read(t.Context(), session.Scope{}); err == nil {
			t.Fatal("ambiguous migration succeeded")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != old {
			t.Fatalf("original file modified: %s err=%v", data, err)
		}
	}
}

func TestResidentMigrationRejectsSyntheticHookIdentity(t *testing.T) {
	_, err := migrateResidentMemory([]byte("[[resident_memories]]\nplatform=''\nactor_id='hook:watch:daily'\ncore='keep'\n"))
	if err == nil {
		t.Fatal("synthetic Hook identity has no actual user")
	}
}
