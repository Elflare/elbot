package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"elbot/internal/config"
)

func TestNewS3BackendRequiresCredentials(t *testing.T) {
	t.Setenv("ELBOT_TEST_S3_ACCESS", "")
	t.Setenv("ELBOT_TEST_S3_SECRET", "")
	_, err := NewS3Backend(context.Background(), config.FileDeliveryConfig{S3AccessKeyEnv: "ELBOT_TEST_S3_ACCESS", S3SecretKeyEnv: "ELBOT_TEST_S3_SECRET"})
	if err == nil {
		t.Fatal("NewS3Backend succeeded without credentials")
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
	access, secret := "access", "secret"
	os.Setenv("ELBOT_TEST_S3_ACCESS", access)
	os.Setenv("ELBOT_TEST_S3_SECRET", secret)
	defer os.Unsetenv("ELBOT_TEST_S3_ACCESS")
	defer os.Unsetenv("ELBOT_TEST_S3_SECRET")

	backend, err := NewS3Backend(context.Background(), config.FileDeliveryConfig{S3Endpoint: server.URL, S3Region: "auto", S3Bucket: "bucket", S3AccessKeyEnv: "ELBOT_TEST_S3_ACCESS", S3SecretKeyEnv: "ELBOT_TEST_S3_SECRET"})
	if err != nil {
		t.Fatalf("new S3 backend: %v", err)
	}
	if _, err := backend.Put(context.Background(), IDPrefix+"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil, 0, "application/octet-stream"); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if gotPath != "/bucket/media/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("request path = %q", gotPath)
	}
}
