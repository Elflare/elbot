package resident

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/session"
)

func TestStoreWriteReadAndDeleteNormal(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "qqonebot", Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "1"}}

	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read empty error = %v", err)
	}
	if err := store.DeleteNormal(context.Background(), scope); err != nil {
		t.Fatalf("DeleteNormal absent: %v", err)
	}
	if _, err := store.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read after absent delete error = %v", err)
	}
	if err := store.WriteCore(context.Background(), scope, "喜欢被称为娅娅"); err != nil {
		t.Fatalf("WriteCore: %v", err)
	}
	if err := store.WriteNormal(context.Background(), scope, "喜欢简短回答"); err != nil {
		t.Fatalf("WriteNormal: %v", err)
	}
	if err := store.WriteCore(context.Background(), scope, " 喜欢被称为娅娅 "); err != nil {
		t.Fatalf("WriteCore existing content: %v", err)
	}
	if err := store.WriteNormal(context.Background(), scope, " 喜欢简短回答 "); err != nil {
		t.Fatalf("WriteNormal existing content: %v", err)
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

func TestStoreClearsExistingEmptyRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	data := []byte("[cli.empty]\ncore = \" \"\nnormal = \" \"\n\n[cli.other]\nnormal = \"keep\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "empty"}}
	if err := store.DeleteNormal(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore(path)
	if _, err := reloaded.Read(context.Background(), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared empty record error = %v", err)
	}
	other, err := reloaded.Read(context.Background(), session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "other"}})
	if err != nil || other.Normal != "keep" {
		t.Fatalf("other memory = %#v, %v", other, err)
	}
}

func TestStoreValidatesExistingContent(t *testing.T) {
	for _, part := range []string{"core", "normal"} {
		t.Run(part, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
			store := NewStore(path)
			write := store.WriteCore
			if part == "normal" {
				write = store.WriteNormal
			}
			if err := write(context.Background(), scope, "two words"); err != nil {
				t.Fatal(err)
			}
			store = NewStoreWithLimits(path, Limits{Core: 1, Normal: 1})
			write = store.WriteCore
			if part == "normal" {
				write = store.WriteNormal
			}
			if err := write(context.Background(), scope, "two words"); err == nil || !strings.Contains(err.Error(), "too long") {
				t.Fatalf("existing content validation error = %v", err)
			}
			memory, err := store.Read(context.Background(), scope)
			if err != nil || (part == "core" && memory.Core != "two words") || (part == "normal" && memory.Normal != "two words") {
				t.Fatalf("memory after rejected write = %#v, %v", memory, err)
			}
		})
	}
}

func TestStoreIsolatesPlatformAndActor(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	qq := session.Scope{Platform: "qqonebot", Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "1"}}
	cli := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "1"}}
	other := session.Scope{Platform: "qqonebot", Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "2"}}
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
	if err := store.WriteCore(context.Background(), session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}, "四个汉字"); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("WriteCore long error = %v", err)
	}
	if err := store.WriteNormal(context.Background(), session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}, "one two three"); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("WriteNormal long error = %v", err)
	}
	if CountUnits("用户 likes short replies") != 5 {
		t.Fatalf("CountUnits mixed = %d", CountUnits("用户 likes short replies"))
	}
}

func TestStoreNormalWriteIgnoresExistingOverLimitCore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	data := []byte("[cli.local]\ncore = \"四个汉字\"\nnormal = \"旧\"\ncreated_at = \"t1\"\nupdated_at = \"t1\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	store := NewStoreWithLimits(path, Limits{Core: 3, Normal: 5})
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
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
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
	if err := store.WriteNormal(context.Background(), scope, "旧记忆"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if memory, err := store.Read(context.Background(), scope); err != nil || memory.Normal != "旧记忆" {
		t.Fatalf("Read cached = %#v, %v", memory, err)
	}

	data := []byte("[cli.local]\ncore = \"核心\"\nnormal = \"外部更新\"\ncreated_at = \"t1\"\nupdated_at = \"t2\"\n")
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
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
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

func TestStoreRejectedWritePreservesRecords(t *testing.T) {
	ctx := context.Background()
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
	other := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "other"}}
	missing := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "missing"}}
	for _, operation := range []string{"core", "normal", "append", "new"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			store := NewStoreWithLimits(path, Limits{Core: 1, Normal: 1})
			for _, target := range []session.Scope{scope, other} {
				if err := store.WriteNormal(ctx, target, "keep"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "core":
				err = store.WriteCore(ctx, scope, "two words")
			case "normal":
				err = store.WriteNormal(ctx, scope, "two words")
			case "append":
				err = store.AppendNormal(ctx, scope, "extra")
			case "new":
				err = store.WriteNormal(ctx, missing, "two words")
			}
			if err == nil || !strings.Contains(err.Error(), "too long") {
				t.Fatalf("rejected write error = %v", err)
			}
			for _, target := range []session.Scope{scope, other} {
				memory, err := store.Read(ctx, target)
				if err != nil || memory != (Memory{Normal: "keep"}) {
					t.Fatalf("Read %s after rejected write = %#v, %v", target.Owner, memory, err)
				}
			}
			if _, err := store.Read(ctx, missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Read missing after rejected write = %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("file changed after rejected write: %v", err)
			}
		})
	}
}

func TestStoreUnchangedWritePreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	data := "[cli.local]\ncore = \"core\"\nnormal = \"normal\"\ncreated_at = \"t1\"\nupdated_at = \"t2\"\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(946684800, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
	for _, operation := range []string{"core", "normal", "delete missing"} {
		switch operation {
		case "core":
			err = store.WriteCore(context.Background(), scope, " core ")
		case "normal":
			err = store.WriteNormal(context.Background(), scope, " normal ")
		case "delete missing":
			err = store.DeleteNormal(context.Background(), session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "missing"}})
		}
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil || string(content) != data || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("%s rewrote unchanged file: %v", operation, err)
		}
	}
}

func TestStoreAppendNormalFromExternalFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want Memory
	}{
		{name: "empty", want: Memory{Normal: "new"}},
		{name: "whitespace", data: " \t\n", want: Memory{Normal: "new"}},
		{name: "comment", data: "# empty memory\n", want: Memory{Normal: "new"}},
		{name: "existing", data: "[cli.local]\ncore = \" core \"\nnormal = \" old \"\n", want: Memory{Core: "core", Normal: "old new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			if err := os.WriteFile(path, []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			store := NewStore(path)
			scope := session.Scope{Platform: "cli", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}}
			if err := store.AppendNormal(context.Background(), scope, " \t "); err == nil {
				t.Fatal("AppendNormal accepted empty content")
			}
			if err := store.AppendNormal(context.Background(), scope, " new "); err != nil {
				t.Fatal(err)
			}
			for _, reader := range []*Store{store, NewStore(path)} {
				memory, err := reader.Read(context.Background(), scope)
				if err != nil || memory != tc.want {
					t.Fatalf("appended memory = %#v, %v; want %#v", memory, err, tc.want)
				}
			}
		})
	}
}

func TestStorePlatformTablesRoundTripAndCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	store := NewStore(path)
	owners := []contextinfo.ActorIdentity{
		{Platform: "telegram", ID: "user:42.娅\"\\\n"},
		{Platform: "telegram", ID: "other"},
		{Platform: "cli", ID: "user:42.娅\"\\\n"},
	}
	for _, owner := range owners {
		// Scope.Platform 是会话平台；常驻记忆必须使用 Owner.Platform。
		scope := session.Scope{Platform: "different", Owner: owner}
		if err := store.WriteNormal(t.Context(), scope, owner.Platform+"/"+owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file tomlFile
	if err := toml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file) != 2 || len(file["telegram"]) != 2 || len(file["cli"]) != 1 {
		t.Fatalf("unexpected platform tables: %#v", file)
	}
	reloaded := NewStore(path)
	for _, owner := range owners {
		memory, err := reloaded.Read(t.Context(), session.Scope{Owner: owner})
		if err != nil || memory.Normal != strings.TrimSpace(owner.Platform+"/"+owner.ID) {
			t.Fatalf("Read %+v = %#v, %v", owner, memory, err)
		}
		record := file[owner.Platform][owner.ID]
		if record.CreatedAt == "" || record.UpdatedAt == "" {
			t.Fatalf("missing timestamps for %+v: %#v", owner, record)
		}
	}
	for i, owner := range owners {
		if err := store.DeleteNormal(t.Context(), session.Scope{Owner: owner}); err != nil {
			t.Fatal(err)
		}
		remaining, err := NewStore(path).loadLocked()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && len(remaining["telegram"]) != 1 {
			t.Fatalf("deleting one user removed siblings: %#v", remaining)
		}
		if i == 1 && (len(remaining) != 1 || len(remaining["cli"]) != 1) {
			t.Fatalf("empty platform not removed or other platform lost: %#v", remaining)
		}
		if i == 2 && len(remaining) != 0 {
			t.Fatalf("cleared file retains tables: %#v", remaining)
		}
	}
}

func TestStoreReadPreservesNewFiles(t *testing.T) {
	for _, data := range []string{"", " \t\n", "# empty memory\n", "[cli.local]\ncore='keep'\n"} {
		path := filepath.Join(t.TempDir(), "memories.toml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Unix(946684800, 0)
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
		store := NewStore(path)
		if _, err := store.loadLocked(); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil || !after.ModTime().Equal(oldTime) {
			t.Fatalf("Read rewrote file: %v", err)
		}
		content, err := os.ReadFile(path)
		if err != nil || string(content) != data {
			t.Fatalf("Read changed file content: %v", err)
		}
		if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Read created migration backup: %v", err)
		}
	}
}

func TestStoreFailedSavePreservesCache(t *testing.T) {
	for _, operation := range []string{"update", "delete", "insert"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memories.toml")
			store := NewStore(path)
			owners := []contextinfo.ActorIdentity{
				{Platform: "cli", ID: "local"},
				{Platform: "cli", ID: "other"},
				{Platform: "telegram", ID: "local"},
			}
			for _, owner := range owners {
				if err := store.WriteNormal(t.Context(), session.Scope{Owner: owner}, "keep"); err != nil {
					t.Fatal(err)
				}
			}
			target := owners[0]
			if operation == "insert" {
				target.ID = "new"
			}
			err := store.update(t.Context(), session.Scope{Owner: target}, func(memory *ResidentMemory) error {
				memory.Normal = "changed"
				if operation == "delete" {
					memory.Normal = ""
				}
				// 加载完成后用目录占据目标路径，使保存阶段的原子重命名失败。
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			if err == nil {
				t.Fatal("save to directory succeeded")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".original", path); err != nil {
				t.Fatal(err)
			}
			for _, reader := range []*Store{store, NewStore(path)} {
				for _, owner := range owners {
					memory, err := reader.Read(t.Context(), session.Scope{Owner: owner})
					if err != nil || memory != (Memory{Normal: "keep"}) {
						t.Fatalf("failed save changed %+v: %#v, %v", owner, memory, err)
					}
				}
				if operation == "insert" {
					if _, err := reader.Read(t.Context(), session.Scope{Owner: target}); !errors.Is(err, ErrNotFound) {
						t.Fatalf("failed save inserted record: %v", err)
					}
				}
			}
		})
	}
}
