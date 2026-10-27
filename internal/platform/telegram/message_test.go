package telegram

import (
	"context"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"path/filepath"
	"testing"
)

func TestStripBotMentionFromCommand(t *testing.T) {
	text, mentioned := stripBotMention("/help@ElBot hi", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "/help hi" {
		t.Fatalf("text = %q", text)
	}
}

func TestStripBotMentionFromText(t *testing.T) {
	text, mentioned := stripBotMention("hello @ElBot", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestStripBotMentionFromTextCaseInsensitive(t *testing.T) {
	text, mentioned := stripBotMention("hello @elbot", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestNormalizeDocument(t *testing.T) {
	msg := message{Document: &document{FileID: "file-id", FileName: "a.txt", MIMEType: "text/plain"}}
	normalized := normalizeMessage(msg)
	if normalized.Text != "[文件]" {
		t.Fatalf("text = %q", normalized.Text)
	}
	if len(normalized.Segments) != 1 || normalized.Segments[0].Name != "a.txt" || normalized.Segments[0].MIMEType != "text/plain" || normalized.Segments[0].PlatformFileID != "file-id" || normalized.Segments[0].URL != "" {
		t.Fatalf("segments = %#v", normalized.Segments)
	}
}

func newTestAdapter(t *testing.T, cfg Config, store storage.Store, history storage.ChatHistoryRepository) *Adapter {
	t.Helper()
	if store == nil {
		db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "store.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store = db
	}
	if history == nil {
		db, err := sqlite.NewChatHistory(context.Background(), filepath.Join(t.TempDir(), "history.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		history = db.Repository()
	}
	return New(cfg, history)
}
