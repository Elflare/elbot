package media

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"elbot/internal/config"
	"elbot/internal/storage/sqlite"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestNewS3BackendRequiresCredentials(t *testing.T) {
	_, err := NewS3Backend(context.Background(), config.FileDeliveryConfig{}, nil)
	if err == nil {
		t.Fatal("NewS3Backend succeeded without credentials")
	}
	empty := credentials.NewStaticCredentialsProvider("", "", "")
	if _, err := NewS3Backend(context.Background(), config.FileDeliveryConfig{}, empty); err == nil {
		t.Fatal("NewS3Backend succeeded with empty credentials")
	}
}

func TestNewConfiguredOnlyRequiresCredentialsForRemoteModes(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewConfigured(ctx, store, t.TempDir(), config.FileDeliveryConfig{Backend: "base64"}, nil); err != nil {
		t.Fatalf("base64 requires S3 credentials: %v", err)
	}
	for _, mode := range []string{"s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := NewConfigured(ctx, store, t.TempDir(), config.FileDeliveryConfig{Backend: mode}, nil); err == nil {
				t.Fatal("remote mode accepted missing credentials")
			}
		})
	}
}

func TestS3BackendUsesCompatibleEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	provider := credentials.NewStaticCredentialsProvider("access", "secret", "")

	backend, err := NewS3Backend(context.Background(), config.FileDeliveryConfig{
		S3Endpoint: server.URL,
		S3Region:   "auto",
		S3Bucket:   "bucket",
	}, provider)
	if err != nil {
		t.Fatalf("new S3 backend: %v", err)
	}
	id := IDPrefix + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := backend.Put(context.Background(), id, bytes.NewReader(nil), 0, "application/octet-stream"); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if gotPath != "/bucket/media/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("request path = %q", gotPath)
	}
}
