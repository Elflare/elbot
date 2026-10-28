package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/media/share"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go"
)

func newTestS3Backend(t *testing.T, endpoint string) *S3Backend {
	t.Helper()
	b, err := NewS3Backend(t.Context(), S3Options{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "bucket",
		Credentials: credentials.NewStaticCredentialsProvider("access", "secret", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestS3BackendRequestsAndSignature(t *testing.T) {
	type request struct {
		method, path, body, mime, authorization string
		size                                    int64
	}
	requests := make(chan request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- request{r.Method, r.URL.Path, string(body), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.ContentLength}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "remote content")
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	b := newTestS3Backend(t, server.URL)
	hash := strings.Repeat("a", 64)
	location, err := b.Put(t.Context(), share.IDPrefix+hash, strings.NewReader("payload"), 7, "text/plain")
	if err != nil || location != "media/"+hash {
		t.Fatalf("Put = %q, %v", location, err)
	}
	got := <-requests
	if got.method != "PUT" || got.path != "/bucket/media/"+hash || got.body != "payload" || got.mime != "text/plain" || got.size != 7 {
		t.Fatalf("Put request = %+v", got)
	}
	if !strings.Contains(got.authorization, "Credential=access/") {
		t.Fatalf("credentials not used: %q", got.authorization)
	}
	// Read, remove and sign must use the supplied key, not regenerate a media key.
	reader, err := b.Open(t.Context(), "selected/object.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || string(data) != "remote content" {
		t.Fatalf("Open = %q, %v", data, err)
	}
	got = <-requests
	if got.method != "GET" || got.path != "/bucket/selected/object.txt" {
		t.Fatalf("Get request = %+v", got)
	}
	if err := b.Remove(t.Context(), "selected/object.txt"); err != nil {
		t.Fatal(err)
	}
	got = <-requests
	if got.method != "DELETE" || got.path != "/bucket/selected/object.txt" {
		t.Fatalf("Delete request = %+v", got)
	}
	signed, err := b.PresignGet(t.Context(), "selected/object.txt", 7*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/bucket/selected/object.txt" || parsed.Query().Get("X-Amz-Expires") != "420" || parsed.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("signed URL = %s", signed)
	}
	select {
	case got := <-requests:
		t.Fatalf("signing made HTTP request: %+v", got)
	default:
	}
}

func TestS3BackendUsesOnlySuppliedOptions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "aws-config")
	if err := os.WriteFile(configPath, []byte("invalid AWS config ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", configPath)
	t.Setenv("AWS_PROFILE", "unconfigured")
	t.Setenv("AWS_REGION", "wrong-region")
	t.Setenv("AWS_ACCESS_KEY_ID", "wrong-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wrong-secret")
	t.Setenv("AWS_ENDPOINT_URL", "http://wrong.invalid")
	t.Setenv("AWS_ENDPOINT_URL_S3", "http://wrong-s3.invalid")
	for _, endpoint := range []string{"", "https://objects.example.test"} {
		t.Run(endpoint, func(t *testing.T) {
			b := newTestS3Backend(t, endpoint)
			signed, err := b.PresignGet(t.Context(), "selected", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(signed)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint == "" {
				if parsed.Host != "bucket.s3.us-east-1.amazonaws.com" || parsed.Path != "/selected" {
					t.Fatalf("default endpoint = %s", signed)
				}
			} else if parsed.Host != "objects.example.test" || parsed.Path != "/bucket/selected" {
				t.Fatalf("custom endpoint = %s", signed)
			}
			credential := parsed.Query().Get("X-Amz-Credential")
			if !strings.HasPrefix(credential, "access/") || !strings.Contains(credential, "/us-east-1/s3/aws4_request") {
				t.Fatalf("signing scope = %q", credential)
			}
		})
	}
}

func TestS3BackendHTTPFailuresPreserveAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
	}))
	defer server.Close()
	b := newTestS3Backend(t, server.URL)
	operations := map[string]func() error{
		"Put": func() error {
			_, err := b.Put(t.Context(), share.IDPrefix+strings.Repeat("a", 64), strings.NewReader("x"), 1, "text/plain")
			return err
		},
		"Open":   func() error { _, err := b.Open(t.Context(), "key"); return err },
		"Remove": func() error { return b.Remove(t.Context(), "key") },
	}
	for name, call := range operations {
		t.Run(name, func(t *testing.T) {
			var apiError smithy.APIError
			if err := call(); !errors.As(err, &apiError) || apiError.ErrorCode() != "AccessDenied" {
				t.Fatalf("API error lost: %v", err)
			}
		})
	}
}

func TestS3BackendCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if b, err := NewS3Backend(ctx, S3Options{}); b != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled constructor = %v, %v", b, err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	b := newTestS3Backend(t, server.URL)
	if _, err := b.Put(ctx, share.IDPrefix+strings.Repeat("b", 64), strings.NewReader("x"), 1, "text/plain"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v", err)
	}
	if _, err := b.Open(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open = %v", err)
	}
	if err := b.Remove(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("canceled operations sent %d requests", requests.Load())
	}
}

func TestS3BackendCancelsInFlightRequest(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	b := newTestS3Backend(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.Open(ctx, "key"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Open = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not exit after cancellation")
	}
}

func TestS3BackendDefersProviderUseAndPreservesSigningError(t *testing.T) {
	failure := errors.New("credentials unavailable")
	calls := 0
	b, err := NewS3Backend(t.Context(), S3Options{
		Region: "us-east-1", Bucket: "bucket",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { calls++; return aws.Credentials{}, failure }),
	})
	if err != nil || calls != 0 {
		t.Fatalf("constructor = %v, provider calls=%d", err, calls)
	}
	if _, err := b.PresignGet(t.Context(), "key", time.Minute); !errors.Is(err, failure) {
		t.Fatalf("signing error lost: %v", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
}
