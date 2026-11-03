package resident

// v0.6.0 迁移测试：从引入新格式的版本起，次版本号再递增两次时随迁移代码删除本文件。
// 补丁版本不计，例如 0.7.0 引入，则在 0.9.0 删除。

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
)

func TestResidentMigrationPreservesRecordsAndWritesOnce(t *testing.T) {
	old := "# original comment\n"
	want := tomlFile{
		"telegram": {
			"user:42.娅": {Core: " keep memory ", Normal: "first\nsecond", CreatedAt: "created", UpdatedAt: "updated"},
			"other":     {Normal: "other user"},
		},
		"cli": {"user:42.娅": {Normal: "other platform"}},
	}
	for platform, memories := range want {
		for id, memory := range memories {
			old += "[[resident_memories]]\n"
			old += "platform='" + platform + "'\nactor_id='" + platform + ":" + id + "'\n"
			data, err := toml.Marshal(memory)
			if err != nil {
				t.Fatal(err)
			}
			old += string(data) + "\n"
		}
	}
	path := filepath.Join(t.TempDir(), "memories.toml")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "user:42.娅"}}
	store := NewStore(path)
	got, err := store.Read(t.Context(), scope)
	if err != nil || got != (Memory{Core: "keep memory", Normal: "first\nsecond"}) {
		t.Fatalf("Read migrated = %+v, %v", got, err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != old {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"resident_memories", "actor_id"} {
		if strings.Contains(string(data), removed) {
			t.Fatalf("migrated file retains %q: %s", removed, data)
		}
	}
	var file tomlFile
	if err := toml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(file, want) || !reflect.DeepEqual(store.cache.file, want) {
		t.Fatalf("migrated records = %#v; cached = %#v", file, store.cache.file)
	}
	oldTime := time.Unix(946684800, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err = NewStore(path).Read(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(oldTime) {
		t.Fatalf("reopening rewrote migrated file: %v", err)
	}
	backup, err = os.ReadFile(path + ".bak")
	if err != nil || string(backup) != old {
		t.Fatalf("reopening changed backup: %v", err)
	}
}

func TestResidentMigrationRejectsInvalidFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{"ambiguous", "[[resident_memories]]\nplatform='telegram'\nactor_id='untyped'\ncore='keep'\n", "ambiguous"},
		{"duplicate legacy", strings.Repeat("[[resident_memories]]\nplatform='telegram'\nactor_id='telegram:1'\ncore='keep'\n", 2), "duplicate"},
		{"hook", "[[resident_memories]]\nplatform=''\nactor_id='hook:watch:daily'\ncore='keep'\n", "no recoverable"},
		{"elnis", "[[resident_memories]]\nplatform='elnis'\nactor_id='elnis:old'\n", "no recoverable"},
		{"mismatched platform", "[[resident_memories]]\nplatform='telegram'\nactor_id='cli:1'\n", "ambiguous"},
		{"unknown field", "[[resident_memories]]\nplatform='cli'\nactor_id='cli:local'\nunknown='keep'\n", "missing"},
		{"invalid structure", "resident_memories='invalid'\n", "toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewStore(path)
			if _, err := store.Read(t.Context(), session.Scope{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Read invalid file = %v; want %q", err, tc.want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.data || store.cache.loaded {
				t.Fatalf("rejected migration changed file/cache: %v", err)
			}
			if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected migration created backup: %v", err)
			}
		})
	}
}

func TestResidentMigrationPreservesExistingBackup(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			old := "[[resident_memories]]\nplatform='cli'\nactor_id='cli:local'\ncore='keep'\n"
			if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			if directory {
				if err := os.Mkdir(path+".bak", 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path+".bak", []byte("existing backup"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewStore(path)
			if _, err := store.Read(t.Context(), session.Scope{}); !errors.Is(err, os.ErrExist) || !strings.Contains(err.Error(), "backup") {
				t.Fatalf("existing backup error = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != old || store.cache.loaded {
				t.Fatalf("failed backup changed source/cache: %v", err)
			}
			if !directory {
				data, err = os.ReadFile(path + ".bak")
				if err != nil || string(data) != "existing backup" {
					t.Fatalf("existing backup overwritten: %v", err)
				}
			}
		})
	}
}

func TestResidentMigrationEmptyLegacyFiles(t *testing.T) {
	data := "resident_memories=[]\n"
	path := filepath.Join(t.TempDir(), "memories.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Read(t.Context(), session.Scope{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read empty legacy = %v", err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != data {
		t.Fatalf("empty legacy backup = %q, %v", backup, err)
	}
}
