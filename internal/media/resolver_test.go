package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type resolverRemote struct {
	LocalBackend
	puts int
}

func (b *resolverRemote) Put(ctx context.Context, id string, r io.Reader, size int64, mime string) (string, error) {
	b.puts++
	return b.LocalBackend.Put(ctx, id, r, size, mime)
}
func (b *resolverRemote) PresignGet(_ context.Context, item *storage.Media, _ time.Duration) (string, error) {
	return "https://s3.test/" + item.ID + "?X-Amz-Signature=temporary", nil
}

func TestResolverRequestTotalAndIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	first, err := m.ImportBytes(ctx, []byte("1234"), Input{Name: "first.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.ImportBytes(ctx, []byte("5678"), Input{Name: "second.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: first.ID}}}, {Role: llm.RoleTool, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: second.ID}}}}
	remote := &resolverRemote{LocalBackend: LocalBackend{Root: t.TempDir()}}
	m.Remote = remote
	for _, tc := range []struct {
		backend               string
		limit                 int64
		wantRemote, wantError bool
	}{
		{"base64", 8, false, false}, {"base64", 7, false, true}, {"hybrid", 8, false, false}, {"hybrid", 7, true, false}, {"s3", 100, true, false},
	} {
		t.Run(tc.backend+"/"+time.Duration(tc.limit).String(), func(t *testing.T) {
			m.FileDelivery.Backend, m.FileDelivery.MaxDirectBase64Bytes = tc.backend, tc.limit
			out, err := m.ResolveForLLM(ctx, input)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil {
				return
			}
			for _, message := range out {
				for _, segment := range message.Segments {
					if strings.HasPrefix(segment.URL, "https://") != tc.wantRemote {
						t.Fatalf("mixed/wrong transport: %#v", out)
					}
					if !tc.wantRemote && !strings.HasPrefix(segment.URL, "data:image/png;base64,") {
						t.Fatal(segment.URL)
					}
				}
			}
		})
	}
	if remote.puts != 2 {
		t.Fatalf("uploads = %d, expected remote reuse", remote.puts)
	}
	for _, message := range input {
		if message.Segments[0].URL != "" {
			t.Fatal("canonical input mutated")
		}
	}
	stored, err := store.Media().Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.SourceURL+stored.ObjectKey+stored.LocalPath, "X-Amz") {
		t.Fatal("signed URL persisted")
	}
	m.FileDelivery.Backend = "base64"
	m.FileDelivery.MaxDirectBase64Bytes = 100
	missing := llm.LLMMessage{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: IDPrefix + strings.Repeat("0", 64)}}}
	out, err := m.ResolveForLLM(ctx, []llm.LLMMessage{missing})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.SegmentsContentText(out[0].Segments), "媒体不可用") {
		t.Fatal(out)
	}
}

func TestImportURLUnknownSizeAndLimits(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte{0, 255, 128, 1})
	}))
	defer server.Close()
	item, err := m.ImportURL(ctx, server.URL+"/a.png?X-Amz-Signature=secret", Input{})
	if err != nil {
		t.Fatal(err)
	}
	if item.Size != 4 || item.SourceURL != "" || item.MIMEType != "image/png" {
		t.Fatalf("metadata: %#v", item)
	}
	m.MaxImportBytes = 3
	for _, source := range []string{server.URL + "/a.png", server.URL + "/missing", "file:///a"} {
		if _, err := m.ImportURL(ctx, source, Input{}); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	invalid := m.Materialize(ctx, []llm.MessageSegment{{Type: llm.SegmentImage, URL: "data:image/png;base64,%%%"}})
	if !strings.Contains(llm.SegmentsContentText(invalid), "媒体不可用") {
		t.Fatal(invalid)
	}
}

func TestLocalPresignDoesNotRecordRemoteObject(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	item, err := m.ImportBytes(ctx, []byte("local only"), Input{Name: "local.png"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PresignGet(ctx, item.ID, time.Hour); err == nil {
		t.Fatal("local backend unexpectedly generated a download URL")
	}
	stored, err := store.Media().Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ObjectKey != "" || stored.LocalPath != item.LocalPath {
		t.Fatalf("local metadata changed: %#v", stored)
	}
}

func TestResolverUploadsLocalMediaAfterBackendChange(t *testing.T) {
	for _, mode := range []string{"s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			root := t.TempDir()
			local := NewManager(store, root, &LocalBackend{Root: root})
			data := []byte("local image before backend change")
			item, err := local.ImportBytes(ctx, data, Input{Name: "old.png", MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			objects := map[string][]byte{}
			puts := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodPut:
					body, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					objects[r.URL.Path] = body
					puts++
				case http.MethodGet:
					body, ok := objects[r.URL.Path]
					if !ok {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(body)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer server.Close()
			t.Setenv("ELBOT_TEST_S3_ACCESS", "access")
			t.Setenv("ELBOT_TEST_S3_SECRET", "secret")
			m, err := NewConfigured(ctx, store, root, config.FileDeliveryConfig{
				Backend: mode, MaxDirectBase64Bytes: 1,
				S3Endpoint: server.URL, S3Region: "auto", S3Bucket: "bucket",
				S3AccessKeyEnv: "ELBOT_TEST_S3_ACCESS", S3SecretKeyEnv: "ELBOT_TEST_S3_SECRET",
			})
			if err != nil {
				t.Fatal(err)
			}
			remote := m.Remote.(*S3Backend)
			options := remote.client.Options()
			options.HTTPClient = server.Client()
			// The test server stores raw bodies without S3 trailer decoding.
			options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			remote.client = s3.New(options)
			remote.presign = s3.NewPresignClient(remote.client)
			input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: item.ID}}}}
			for i := 0; i < 2; i++ {
				out, err := m.ResolveForLLM(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				response, err := server.Client().Get(out[0].Segments[0].URL)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || string(body) != string(data) {
					t.Fatalf("presigned download: HTTP %d, body %q, error %v", response.StatusCode, body, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if puts != 1 {
				t.Fatalf("uploads = %d, want one reused remote object", puts)
			}
			stored, err := store.Media().Get(ctx, item.ID)
			if err != nil || stored.Backend != "local" || stored.LocalPath == "" || stored.ObjectKey == "" {
				t.Fatalf("metadata = %#v, error %v", stored, err)
			}
		})
	}
}
