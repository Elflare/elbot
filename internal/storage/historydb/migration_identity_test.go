package historydb

import (
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

func TestChatHistoryMigrationSeparatesConversationKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := share.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(legacyChatHistorySQL); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"private", "group"} {
		if _, err = db.Exec(`INSERT INTO chat_messages(id,platform,platform_scope_id,scope_type,platform_message_id,sender_id,text,created_at) VALUES(?,'qqonebot',?,?,'same','sender',?,?)`, kind, kind+":room:9", kind, kind, share.FormatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO platform_message_aliases VALUES('qqonebot','private:room:9','alias','same',?),('qqonebot','group:room:9','alias','same',?)`, share.FormatTime(time.Now()), share.FormatTime(time.Now())); err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec("UPDATE sqlite_sequence SET seq=100 WHERE name='chat_messages'"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []contextinfo.ChatType{"private", "group"} {
			row, err := store.GetByPlatformMessage(t.Context(), "qqonebot", kind, "room:9", "same")
			if err != nil || row.Text != string(kind) {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			id, _, err := store.ResolvePlatformMessageAlias(t.Context(), "qqonebot", kind, "room:9", "alias")
			if err != nil || id != "same" {
				t.Fatalf("alias=%q err=%v", id, err)
			}
		}

		var sequence int64
		if err = store.history.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='chat_messages'").Scan(&sequence); err != nil || sequence != 100 {
			t.Fatalf("sequence=%d err=%v", sequence, err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
