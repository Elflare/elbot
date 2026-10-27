// message_test.go 验证入站消息归一化并提供唯一构造入口的测试夹具。

package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
	"elbot/internal/storage/sqlite"
	"github.com/pelletier/go-toml/v2"
	"path/filepath"
	"testing"
	"time"
)

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

func newTestStream(t *testing.T, a *Adapter, scope string) delivery.MessageStream {
	t.Helper()
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ScopeID: scope}})
	s, err := a.StartStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestAdapter(t *testing.T, cfg Config, history share.ChatHistoryWriter) *Adapter {
	t.Helper()
	if history == nil {
		db, err := sqlite.NewChatHistory(context.Background(), filepath.Join(t.TempDir(), "history.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		history = db.Repository()
	}
	cfg.Enabled = true
	if cfg.BotToken == "" && cfg.BotTokenEnv == "" {
		cfg.BotToken = "test-token"
	}
	raw := map[string]any{}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	a, err := NewFromPlatformConfig(raw, Deps{
		CommonConfig: share.CommonConfig{Superadmins: cfg.Superadmins, CommandPrefixes: cfg.CommandPrefixes, ConfigDir: cfg.ConfigEnvDir},
		ChatHistory:  history,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})
	return a
}
