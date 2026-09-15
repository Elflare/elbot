package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
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
			out, cleanup, err := m.ResolveForLLM(ctx, input)
			cleanup()
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
	out, cleanup, err := m.ResolveForLLM(ctx, []llm.LLMMessage{missing})
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.SegmentsContentText(out[0].Segments), "媒体不可用") {
		t.Fatal(out)
	}
}

func TestImportCompressionAndResolverReuse(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	m.Media = config.MediaConfig{LLMImageCompressionThresholdBytes: 1024 * 1024, LLMImageMaxLength: 32}
	data := testPNG(t, 80, 40)
	materialized := m.Materialize(ctx, []llm.MessageSegment{{Type: llm.SegmentImage, Name: "large.png", MIMEType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}})
	segment := materialized[0]
	if segment.MediaID == "" || segment.Name != "large.jpg" || segment.MIMEType != "image/jpeg" || segment.URL != "" {
		t.Fatalf("materialized = %#v", segment)
	}
	storedData, item, err := m.Read(ctx, segment.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	width, height, err := imageDimensions(storedData)
	if err != nil || width >= 32 || height >= 32 || item.Size != int64(len(storedData)) || item.ID != fmt.Sprintf("%s%x", IDPrefix, sha256.Sum256(storedData)) {
		t.Fatalf("compressed media = %#v, dimensions %dx%d, error %v", item, width, height, err)
	}
	originalID := fmt.Sprintf("%s%x", IDPrefix, sha256.Sum256(data))
	if _, err := m.Metadata(ctx, originalID); err != storage.ErrNotFound {
		t.Fatalf("original persisted: %v", err)
	}
	remote := &resolverRemote{LocalBackend: LocalBackend{Root: t.TempDir()}}
	m.Remote = remote
	input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{segment, segment}}}
	// Tightening import limits must not recompress already stored media.
	m.Media = config.MediaConfig{LLMImageCompressionThresholdBytes: 1, LLMImageMaxLength: 1}
	for _, mode := range []string{"base64", "hybrid", "s3"} {
		m.FileDelivery.Backend = mode
		m.FileDelivery.MaxDirectBase64Bytes = item.Size * 2
		for attempt := 0; attempt < 2; attempt++ {
			out, cleanup, err := m.ResolveForLLM(ctx, input)
			cleanup()
			cleanup()
			if err != nil {
				t.Fatal(err)
			}
			got := out[0].Segments[0]
			want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(storedData)
			if mode == "s3" {
				want = "https://s3.test/" + item.ID + "?X-Amz-Signature=temporary"
			}
			if got.URL != want || got.MediaID != item.ID || out[0].Segments[1].URL != want {
				t.Fatalf("%s resolved = %#v", mode, out)
			}
		}
	}
	m.FileDelivery.Backend, m.FileDelivery.MaxDirectBase64Bytes = "hybrid", item.Size*2-1
	out, cleanup, err := m.ResolveForLLM(ctx, input)
	cleanup()
	if err != nil || !strings.HasPrefix(out[0].Segments[0].URL, "https://s3.test/"+item.ID) || remote.puts != 1 {
		t.Fatalf("remote reuse = %#v, uploads %d, error %v", out, remote.puts, err)
	}
	if input[0].Segments[0].URL != "" {
		t.Fatal("canonical input mutated")
	}
}

func TestImportImageLimits(t *testing.T) {
	data := testPNG(t, 80, 40)
	for _, tc := range []struct {
		name                  string
		data                  []byte
		mime                  string
		bytes                 int64
		length                int
		importLimit           int64
		compressed, wantError bool
	}{
		{name: "unchanged", data: data, bytes: int64(len(data)) + 1, length: 81},
		{name: "byte boundary", data: data, bytes: int64(len(data)), length: 81},
		{name: "bytes", data: append(append([]byte(nil), data...), make([]byte, 4096)...), bytes: 2048, length: 81, compressed: true},
		{name: "dimension boundary", data: data, bytes: 4096, length: 80, compressed: true},
		{name: "generic MIME image", data: data, mime: "application/octet-stream", bytes: 4096, length: 32, compressed: true},
		{name: "non image", data: []byte("ordinary file"), mime: "text/plain", bytes: 2, length: 2},
		{name: "invalid image", data: []byte("invalid image"), mime: "image/png", bytes: 2, length: 32, wantError: true},
		{name: "impossible compression", data: data, bytes: 1, length: 32, wantError: true},
		{name: "hard import limit", data: data, bytes: 4096, length: 32, importLimit: int64(len(data)) - 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend := &resolverRemote{LocalBackend: LocalBackend{Root: t.TempDir()}}
			m := NewManager(store, backend.Root, backend)
			m.Media = config.MediaConfig{LLMImageCompressionThresholdBytes: tc.bytes, LLMImageMaxLength: tc.length}
			if tc.importLimit > 0 {
				m.MaxImportBytes = tc.importLimit
			}
			item, err := m.ImportReader(ctx, bytes.NewReader(tc.data), -1, Input{Name: "input", MIMEType: tc.mime})
			if (err != nil) != tc.wantError {
				t.Fatalf("import error = %v", err)
			}
			if tc.wantError {
				if backend.puts != 0 {
					t.Fatal("failed import persisted an object")
				}
				return
			}
			got, _, err := m.Read(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.compressed {
				w, h, err := imageDimensions(got)
				if err != nil || w >= tc.length || h >= tc.length || int64(len(got)) >= tc.bytes || item.MIMEType != "image/jpeg" || item.Name != "input.jpg" {
					t.Fatalf("compressed = %#v, %dx%d, %v", item, w, h, err)
				}
			} else if !bytes.Equal(got, tc.data) {
				t.Fatal("uncompressed content changed")
			}
			again, err := m.ImportBytes(ctx, tc.data, Input{Name: "input", MIMEType: tc.mime})
			if err != nil || again.ID != item.ID || backend.puts != 1 {
				t.Fatalf("dedup = %#v, %v, puts %d", again, err, backend.puts)
			}
		})
	}
}

func TestResolverDoesNotCompressExistingMedia(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	data := testPNG(t, 80, 40)
	item, err := m.ImportBytes(ctx, data, Input{Name: "old.png"})
	if err != nil {
		t.Fatal(err)
	}
	m.Media = config.MediaConfig{LLMImageCompressionThresholdBytes: 1, LLMImageMaxLength: 1}
	input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: item.ID}}}}
	out, cleanup, err := m.ResolveForLLM(ctx, input)
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if got := out[0].Segments[0]; got.MediaID != item.ID || got.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("old media changed = %#v", got)
	}
}

func TestImportURLAndFileCompressIdentically(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	m.Media.LLMImageMaxLength = 32
	data := testPNG(t, 80, 40)
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	fromURL, err := m.ImportURL(ctx, server.URL+"/photo.png", Input{})
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := m.ImportFile(ctx, path, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if fromURL.ID != fromFile.ID || fromURL.Name != "photo.jpg" || fromURL.MIMEType != "image/jpeg" {
		t.Fatalf("URL = %#v, file = %#v", fromURL, fromFile)
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 3), B: uint8(x + y), A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
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
	var requestedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedSignature = r.URL.Query().Get("X-Amz-Signature")
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
	if requestedSignature != "secret" {
		t.Fatalf("download request was sanitized: signature = %q", requestedSignature)
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
			provider := credentials.NewStaticCredentialsProvider("access", "secret", "")
			m, err := NewConfigured(ctx, store, root, config.FileDeliveryConfig{
				Backend: mode, MaxDirectBase64Bytes: 1,
				S3Endpoint: server.URL, S3Region: "auto", S3Bucket: "bucket",
			}, provider)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := m.remoteBackend(ctx)
			if err != nil {
				t.Fatal(err)
			}
			remote := backend.(*S3Backend)
			options := remote.client.Options()
			options.HTTPClient = server.Client()
			// The test server stores raw bodies without S3 trailer decoding.
			options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			remote.client = s3.New(options)
			remote.presign = s3.NewPresignClient(remote.client)
			input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: item.ID}}}}
			for i := 0; i < 2; i++ {
				out, cleanup, err := m.ResolveForLLM(ctx, input)
				cleanup()
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

func TestMaterializeAndResolveSanitizeMediaNames(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})

	sourceName := "https://example.com/private/photo.png?rkey=name-secret"
	materialized := m.Materialize(ctx, []llm.MessageSegment{{
		Type: llm.SegmentImage,
		URL:  "data:image/png;base64,aW1hZ2U=",
		Name: sourceName,
	}})
	if len(materialized) != 1 || materialized[0].MediaID == "" || materialized[0].URL != "" || materialized[0].Name != "photo.png" {
		t.Fatalf("materialized = %#v", materialized)
	}

	input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{
		Type:    llm.SegmentImage,
		MediaID: materialized[0].MediaID,
		Name:    sourceName,
	}}}}
	m.FileDelivery.Backend = "base64"
	m.FileDelivery.MaxDirectBase64Bytes = 1024
	resolved, cleanup, err := m.ResolveForLLM(ctx, input)
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Segments[0].Name != "photo.png" || strings.Contains(llm.SegmentsContentText(resolved[0].Segments), "name-secret") {
		t.Fatalf("resolved = %#v", resolved)
	}
	if input[0].Segments[0].Name != sourceName {
		t.Fatal("canonical input was mutated")
	}

	missing := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{
		Type:    llm.SegmentImage,
		MediaID: IDPrefix + strings.Repeat("f", 64),
		Name:    "https://example.com/missing.png?rkey=missing-secret",
	}}}}
	resolved, cleanup, err = m.ResolveForLLM(ctx, missing)
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	text := llm.SegmentsContentText(resolved[0].Segments)
	if strings.Contains(text, "missing-secret") || !strings.Contains(text, "missing.png") {
		t.Fatalf("unavailable text = %q", text)
	}
}
