package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
)

func TestResolveFileDeliveryCredentials(t *testing.T) {
	const accessName = "ELBOT_TEST_MEDIA_ACCESS"
	const secretName = "ELBOT_TEST_MEDIA_SECRET"
	for _, mode := range []string{"s3", "hybrid"} {
		for _, process := range []bool{false, true} {
			t.Run(mode+"/process="+map[bool]string{false: "false", true: "true"}[process], func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv(accessName, "")
				t.Setenv(secretName, "")
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(accessName+"=dotenv-access\n"+secretName+"=dotenv-secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				wantAccess, wantSecret := "dotenv-access", "dotenv-secret"
				if process {
					wantAccess, wantSecret = "process-access", "process-secret"
					t.Setenv(accessName, wantAccess)
					t.Setenv(secretName, wantSecret)
				}
				provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
					Backend: mode, S3AccessKeyEnv: accessName, S3SecretKeyEnv: secretName,
				}, dir)
				if err != nil {
					t.Fatal(err)
				}
				value, err := provider.Retrieve(t.Context())
				if err != nil || value.AccessKeyID != wantAccess || value.SecretAccessKey != wantSecret {
					t.Fatalf("unexpected credentials or retrieval error: %v", err)
				}
			})
		}
	}
}

func TestResolveFileDeliveryCredentialsFailures(t *testing.T) {
	const accessName = "ELBOT_TEST_MEDIA_MISSING_ACCESS"
	const secretName = "ELBOT_TEST_MEDIA_MISSING_SECRET"
	for _, tc := range []struct {
		name, accessEnv, secretEnv, access, secret, want string
	}{
		{"empty access name", "", secretName, "", "", "access key environment variable name is empty"},
		{"empty secret name", accessName, "", "private-access", "", "secret key environment variable name is empty"},
		{"missing access", accessName, secretName, "", "private-secret", accessName},
		{"missing secret", accessName, secretName, "private-access", "", secretName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(accessName, tc.access)
			t.Setenv(secretName, tc.secret)
			provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
				Backend: "s3", S3AccessKeyEnv: tc.accessEnv, S3SecretKeyEnv: tc.secretEnv,
			}, t.TempDir())
			if provider != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected credential configuration failure: %v", err)
			}
			if strings.Contains(err.Error(), "private-access") || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("error leaked credentials")
			}
		})
	}
}

func TestResolveFileDeliveryCredentialsSkipsS3ForBase64(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{Backend: "base64"}, file)
	if err != nil || provider != nil {
		t.Fatalf("base64 tried resolving credentials: %v", err)
	}
	t.Setenv("ELBOT_TEST_MEDIA_BAD_DIR", "")
	_, err = resolveFileDeliveryCredentials(config.FileDeliveryConfig{
		Backend: "s3", S3AccessKeyEnv: "ELBOT_TEST_MEDIA_BAD_DIR",
	}, file)
	if err == nil {
		t.Fatal("remote mode did not report dotenv read error")
	}
}

func TestNewMediaCenterEarlyFailure(t *testing.T) {
	for _, mode := range []string{"canceled", "unknown", "s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.FileDelivery.Backend = mode
			cfg.FileDelivery.S3AccessKeyEnv = ""
			before := *cfg
			ctx := t.Context()
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			// 无需 Store 即可拒绝取消或无效配置；此时不应创建对象与传输组件。
			center, err := NewMediaCenter(ctx, cfg, MediaCenterDeps{})
			if err == nil || center == nil || center.objects != nil || center.transfer != nil || center.chatHistory != nil {
				t.Fatalf("unexpected early construction result: %v, %v", center, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if mode == "unknown" && !strings.Contains(err.Error(), "unsupported media backend") {
				t.Fatalf("unexpected mode error: %v", err)
			}
			if (mode == "s3" || mode == "hybrid") && !strings.Contains(err.Error(), "media credentials") {
				t.Fatalf("unexpected credential error: %v", err)
			}
			if !reflect.DeepEqual(*cfg, before) {
				t.Fatal("constructor changed caller configuration")
			}
		})
	}
}

type unavailableHistory struct{ cause error }

func (h unavailableHistory) GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error) {
	return nil, h.cause
}

func TestNewMediaCenterReconcilesHistoryAndRetainsBorrowedStores(t *testing.T) {
	for _, state := range []string{"current", "missing", "replaced", "unavailable", "without history"} {
		t.Run(state, func(t *testing.T) {
			store := testutil.NewSQLiteStore(t)
			history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = history.Close() })
			row := &storage.ChatMessage{ID: "missing-history", Platform: "test", PlatformScopeID: "scope", PlatformMessageID: "message"}
			if state != "missing" && state != "without history" {
				if err := history.Repository().Append(t.Context(), row); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Default()
			cfg.Sandbox.Root = filepath.Join(t.TempDir(), "sandbox")
			cfg.FileDelivery.Backend = "base64"
			seed, err := NewMediaCenter(t.Context(), cfg, MediaCenterDeps{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = seed.Close(context.Background()) })
			item, err := seed.ImportBytes(t.Context(), []byte("history media"), Input{Name: "file.txt", MIMEType: "text/plain"}, ImportLimits{})
			if err != nil {
				t.Fatal(err)
			}
			key := HistoryMessage{ID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID}
			if state == "replaced" {
				key.ID = "previous-history"
			}
			if err := seed.AssociateHistory(t.Context(), key, 1, "file", item.ID); err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(t.Context()); err != nil {
				t.Fatal(err)
			}

			failure := errors.New("history unavailable")
			var query History = history.Repository()
			switch state {
			case "unavailable":
				query = unavailableHistory{cause: failure}
			case "without history":
				query = nil
			}
			center, err := NewMediaCenter(t.Context(), cfg, MediaCenterDeps{Store: store, ChatHistory: query})
			if center == nil {
				t.Fatal("constructor lost partial ownership")
			}
			t.Cleanup(func() { _ = center.Close(context.Background()) })
			if state == "unavailable" {
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "reconcile history media") {
					t.Fatalf("reconciliation error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			items, err := store.Media().FindHistory(t.Context(), key.Platform, key.ScopeID, key.MessageID)
			want := 1
			if state == "missing" || state == "replaced" {
				want = 0
			}
			if err != nil || len(items) != want {
				t.Fatalf("history associations=%v error=%v", items, err)
			}
			if err := center.Close(t.Context()); err != nil {
				t.Fatal("cannot close constructed or partial media", err)
			}
			if _, err := store.Media().Get(t.Context(), item.ID); err != nil {
				t.Fatal("Media closed borrowed media store", err)
			}
			_, err = history.Repository().GetByPlatformMessage(t.Context(), key.Platform, key.ScopeID, key.MessageID)
			if state == "missing" || state == "without history" {
				if !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("history lookup = %v", err)
				}
			} else if err != nil {
				t.Fatal("Media closed borrowed history store", err)
			}
		})
	}
}
