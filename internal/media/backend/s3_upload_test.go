package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/media/share"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type uploadTestReader struct {
	io.Reader
	closed bool
}

func (r *uploadTestReader) Close() error { r.closed = true; return nil }

type uploadTestSeeker struct {
	*strings.Reader
	closed bool
}

func (r *uploadTestSeeker) Close() error { r.closed = true; return nil }

func TestS3BackendNonSeekableUploadAndRetry(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		for _, attempts := range []int32{1, 2} {
			t.Run(fmt.Sprintf("%s/attempts=%d", protocol, attempts), func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("TMPDIR", dir)
				var requests atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body io.Reader = r.Body
					if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
						body = httputil.NewChunkedReader(body)
					}
					data, err := io.ReadAll(body)
					if err != nil || string(data) != "payload" {
						t.Errorf("request body = %q, %v", data, err)
					}
					if requests.Add(1) < attempts {
						w.Header().Set("Content-Type", "application/xml")
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `<Error><Code>InternalError</Code><Message>retry</Message></Error>`)
						return
					}
					w.WriteHeader(http.StatusOK)
				}))
				if protocol == "https" {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				b := newTestS3Backend(t, server.URL)
				options := b.client.Options()
				options.HTTPClient = server.Client()
				options.Retryer = retry.NewStandard(func(o *retry.StandardOptions) {
					o.MaxAttempts = 2
					o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
				})
				b.client = s3.New(options)
				input := &uploadTestReader{Reader: strings.NewReader("payload")}
				defer input.Close()
				hash := strings.Repeat("a", 64)
				location, err := b.Put(t.Context(), share.IDPrefix+hash, input, 7, "text/plain")
				if err != nil || location != "media/"+hash {
					t.Fatalf("Put = %q, %v", location, err)
				}
				if requests.Load() != attempts {
					t.Fatalf("requests = %d, want %d", requests.Load(), attempts)
				}
				if input.closed {
					t.Error("Put closed borrowed input")
				}
				assertUploadTempsRemoved(t, dir)
			})
		}
	}
}

func TestS3BackendSeekableUploadPreservesOwnershipAndOffset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "payload" {
			t.Errorf("body = %q, %v", data, err)
		}
		assertUploadTempsRemoved(t, dir)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	b := newTestS3Backend(t, server.URL)
	input := &uploadTestSeeker{Reader: strings.NewReader("prefixpayload")}
	defer input.Close()
	if _, err := input.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(t.Context(), share.IDPrefix+strings.Repeat("a", 64), input, 7, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if input.closed {
		t.Fatal("Put closed borrowed seekable input")
	}
	assertUploadTempsRemoved(t, dir)
}

func TestS3BackendUploadFailureRemovesTemporaryFile(t *testing.T) {
	for _, stage := range []string{"read", "cancel before read", "cancel after read", "cancel during upload", "upload"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if stage == "cancel during upload" {
					cancel()
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
			}))
			defer server.Close()
			b := newTestS3Backend(t, server.URL)
			failure := errors.New("source read failed")
			input := &uploadTestReader{Reader: strings.NewReader("payload")}
			defer input.Close()
			switch stage {
			case "read":
				input.Reader = readerFunc(func(p []byte) (int, error) { return copy(p, "partial"), failure })
			case "cancel before read":
				cancel()
				input.Reader = readerFunc(func([]byte) (int, error) { t.Error("read after cancellation"); return 0, io.EOF })
			case "cancel after read":
				input.Reader = readerFunc(func(p []byte) (int, error) { cancel(); return copy(p, "payload"), io.EOF })
			}
			location, err := b.Put(ctx, share.IDPrefix+strings.Repeat("a", 64), input, 7, "text/plain")
			if err == nil || location != "" {
				t.Fatalf("Put = %q, %v", location, err)
			}
			if stage == "read" && !errors.Is(err, failure) {
				t.Fatalf("read error lost: %v", err)
			}
			if strings.HasPrefix(stage, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error lost: %v", err)
			}
			if stage != "upload" && stage != "cancel during upload" && requests.Load() != 0 {
				t.Fatalf("preparation failure sent %d requests", requests.Load())
			}
			if (stage == "upload" || stage == "cancel during upload") && requests.Load() != 1 {
				t.Fatalf("upload did not reach server: %v", err)
			}
			if input.closed {
				t.Error("Put closed borrowed input")
			}
			assertUploadTempsRemoved(t, dir)
		})
	}
}

func assertUploadTempsRemoved(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary files remain: %v, %v", entries, err)
	}
}
