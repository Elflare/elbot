package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/testutil"
)

func TestMediaCenterTransferWiring(t *testing.T) {
	for _, mode := range []string{"base64", "s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			puts, gets := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodPut:
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					objects[r.URL.Path] = data
					puts++
				case http.MethodGet:
					gets++
					data, ok := objects[r.URL.Path]
					if !ok {
						w.Header().Set("Content-Type", "application/xml")
						w.WriteHeader(404)
						_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
						return
					}
					_, _ = w.Write(data)
				default:
					t.Errorf("unexpected method %s", r.Method)
				}
			}))
			t.Cleanup(server.Close)
			cfg := config.Default()
			cfg.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			cfg.Sandbox.Root = filepath.Join(t.TempDir(), "sandbox")
			cfg.FileDelivery.Backend = mode
			cfg.FileDelivery.MaxDirectBase64Bytes = 100
			if mode == "hybrid" {
				cfg.FileDelivery.MaxDirectBase64Bytes = 1
			}
			cfg.FileDelivery.S3Endpoint = server.URL
			cfg.FileDelivery.S3Region = "us-east-1"
			cfg.FileDelivery.S3Bucket = "bucket"
			cfg.FileDelivery.S3AccessKeyEnv = "ELBOT_TRANSFER_TEST_ACCESS"
			cfg.FileDelivery.S3SecretKeyEnv = "ELBOT_TRANSFER_TEST_SECRET"
			t.Setenv(cfg.FileDelivery.S3AccessKeyEnv, "access")
			t.Setenv(cfg.FileDelivery.S3SecretKeyEnv, "secret")
			store := testutil.NewSQLiteStore(t)
			var usageID UsageID
			m, err := NewMediaCenter(t.Context(), cfg, MediaCenterDeps{Store: store})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if usageID != "" {
					m.PublishUsageFinished(ctx, UsageFinishedEvent{UsageIDs: []UsageID{usageID}})
				}
				if err := m.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			info, err := m.ImportBytes(t.Context(), []byte("payload"), Input{Name: "report%2520.txt", MIMEType: "text/plain"}, ImportLimits{})
			if err != nil || info.Name != "report%20.txt" {
				t.Fatalf("imported=%+v err=%v", info, err)
			}
			id := info.ID
			segments := []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: id, Name: info.Name, MIMEType: info.MIMEType}}
			prepared, usageID, err := m.AcquireForLLM(t.Context(), []llm.LLMMessage{{Segments: append(segments, segments[0])}})
			if err != nil {
				t.Fatal(err)
			}
			refs, err := store.MediaReferences().ListMediaIDs(t.Context(), id)
			if err != nil || len(refs) != 1 {
				t.Fatalf("holds=%d %v", len(refs), err)
			}
			value := prepared[0].Segments[0].URL
			if prepared[0].Segments[0].Name != "report%20.txt" || prepared[0].Segments[1].URL != value {
				t.Fatal("name re-cleaned or delivery duplicated")
			}
			if mode == "base64" {
				if !strings.HasPrefix(value, "data:text/plain;base64,") {
					t.Fatal(value)
				}
			} else if !strings.HasPrefix(value, server.URL) || !strings.Contains(value, "X-Amz-Signature") {
				t.Fatal(value)
			}
			m.PublishUsageFinished(t.Context(), UsageFinishedEvent{UsageIDs: []UsageID{usageID}})
			usageID = ""
			if _, err := m.Cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			source, usageID, err := m.ResolveForOutput(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(source.Path)
			if err != nil || string(data) != "payload" {
				t.Fatalf("output=%q %v", data, err)
			}
			refs, err = store.MediaReferences().ListMediaIDs(t.Context(), id)
			if err != nil || len(refs) != 1 {
				t.Fatalf("output holds=%d %v", len(refs), err)
			}
			m.PublishUsageFinished(t.Context(), UsageFinishedEvent{UsageIDs: []UsageID{usageID}})
			usageID = ""
			if _, err := m.Cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			refs, err = store.MediaReferences().ListMediaIDs(t.Context(), id)
			if err != nil || len(refs) != 0 {
				t.Fatalf("leaked holds=%d %v", len(refs), err)
			}
			if _, err := os.Stat(source.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("output retained")
			}
			mu.Lock()
			defer mu.Unlock()
			if mode == "base64" && (puts != 0 || gets != 0) || mode == "hybrid" && (puts != 1 || gets != 0) || mode == "s3" && (puts != 1 || gets != 1) {
				t.Fatalf("remote puts=%d gets=%d", puts, gets)
			}
		})
	}
}

func TestMediaCenterHistoryAndBoundedImport(t *testing.T) {
	cfg := config.Default()
	cfg.FileDelivery.Backend = "base64"
	cfg.Sandbox.Root = filepath.Join(t.TempDir(), "sandbox")
	store := testutil.NewSQLiteStore(t)
	m, err := NewMediaCenter(t.Context(), cfg, MediaCenterDeps{Store: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportReader(t.Context(), strings.NewReader("body"), -1, Input{}, ImportLimits{MaxImportBytes: 3}); err == nil {
		t.Fatal("ignored decoded byte limit")
	}
	row := HistoryMessage{ID: "history", Platform: "p", ScopeID: "s", MessageID: "m", Media: []PlatformMedia{{Type: "file", PlatformFileID: "native"}}}
	calls := 0
	resolver := func(context.Context, string, PlatformMedia, int64) (delivery.Source, error) {
		calls++
		return delivery.Source{Data: []byte("history content"), MIMEType: "text/plain"}, nil
	}
	requests := []HistoryMediaRequest{{Message: row, Index: 1}, {Message: row, Index: 1}}
	first, err := m.GetHistoryMediaBatch(t.Context(), requests, resolver, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || first[0].Err != nil || first[1].Media != first[0].Media || calls != 1 {
		t.Fatalf("first=%+v err=%v calls=%d", first, err, calls)
	}
	again, err := m.GetHistoryMediaBatch(t.Context(), requests, resolver, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || again[0].Err != nil || again[0].Media != first[0].Media || calls != 1 {
		t.Fatalf("again=%+v err=%v calls=%d", again, err, calls)
	}
}
