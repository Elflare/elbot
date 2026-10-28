package backend

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/smithy-go"
)

func TestS3MissingContentClassification(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NotFound", "NoSuchBucket", "AccessDenied"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				status := http.StatusNotFound
				if code == "AccessDenied" {
					status = http.StatusForbidden
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, fmt.Sprintf("<Error><Code>%s</Code><Message>test</Message></Error>", code))
			}))
			defer server.Close()
			_, err := newTestS3Backend(t, server.URL).Open(t.Context(), "object")
			if errors.Is(err, fs.ErrNotExist) != (code == "NoSuchKey" || code == "NotFound") {
				t.Fatalf("classification: %v", err)
			}
			var api smithy.APIError
			if !errors.As(err, &api) || api.ErrorCode() != code {
				t.Fatalf("original error lost: %v", err)
			}
		})
	}
}
