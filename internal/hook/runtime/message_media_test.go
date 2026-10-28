package runtime

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hookoutput "elbot/internal/hook/share/output"
)

func TestMessageMediaImportsOnceAndOwnsBatchLeases(t *testing.T) {
	m, center, dir, store := testMediaRuntime(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "100%.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data.Bytes())
	}))
	defer server.Close()
	segments, err := m.BuildMessageSegments(t.Context(), []hookoutput.MessageSegment{
		{Type: "image", URL: strings.Replace(server.URL, "http:", "HTTP:", 1)},
		{Type: "image", Path: "100%.png"},
		{Type: "image", Base64: base64.StdEncoding.EncodeToString(data.Bytes()), MIMEType: "image/png"},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 {
		t.Fatal(segments)
	}
	for _, segment := range segments {
		if segment.MediaID == "" || segment.MediaID != segments[0].MediaID || segment.URL != "" {
			t.Fatal(segment)
		}
		if _, err := center.Info(t.Context(), segment.MediaID); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := store.MediaReferences().ListByOwner(t.Context(), "hook", m.media.owner)
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	_, err = m.BuildMessageSegments(t.Context(), []hookoutput.MessageSegment{
		{Type: "image", URL: segments[0].MediaID},
		{Type: "image", Path: "missing.png"},
	}, dir)
	if err == nil {
		t.Fatal("missing file accepted")
	}
	refs, err = store.MediaReferences().ListByOwner(t.Context(), "hook", m.media.owner)
	if err != nil || len(refs) != 1 {
		t.Fatal("partial batch lost lease", refs, err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListByOwner(t.Context(), "hook", m.media.owner)
	if err != nil || len(refs) != 0 {
		t.Fatal("lease survived runtime close", refs, err)
	}
	if _, err := m.BuildMessageSegments(t.Context(), []hookoutput.MessageSegment{{Type: "image", URL: segments[0].MediaID}}, dir); err != ErrClosed {
		t.Fatal(err)
	}
}
