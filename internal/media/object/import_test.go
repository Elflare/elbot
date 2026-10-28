package object

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/media/share"
)

func TestImportURLAndFile(t *testing.T) {
	m, _, _ := newTestManager(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			<-r.Context().Done()
		case "/failed":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.Header().Set("Content-Type", "Text/Plain; charset=utf-8")
			_, _ = w.Write([]byte("download"))
		}
	}))
	defer server.Close()
	item, err := m.ImportURL(t.Context(), server.URL+"/name%252Fpart.txt?token=secret", share.Input{}, share.ImportLimits{})
	if err != nil || item.Name != "name%2Fpart.txt" || item.MIMEType != "text/plain" {
		t.Fatalf("download = %#v %v", item, err)
	}
	record, _ := m.media.Get(t.Context(), item.ID)
	if record.SourceURL != "" {
		t.Fatalf("sensitive URL stored: %q", record.SourceURL)
	}
	_, err = m.ImportURL(t.Context(), server.URL+"/slow?token=secret", share.Input{}, share.ImportLimits{DownloadTimeout: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("timeout = %v", err)
	}
	if _, err = m.ImportURL(t.Context(), server.URL+"/failed", share.Input{}, share.ImportLimits{}); err == nil {
		t.Fatal("HTTP failure accepted")
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("file input"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err = m.ImportFile(t.Context(), path, share.Input{}, share.ImportLimits{})
	if err != nil || item.Name != "source.txt" {
		t.Fatalf("file = %#v %v", item, err)
	}
	if _, err := m.ImportFile(t.Context(), filepath.Dir(path), share.Input{}, share.ImportLimits{}); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestImageImportCompressionAndValidation(t *testing.T) {
	m, _, _ := newTestManager(t)
	source := image.NewNRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, source); err != nil {
		t.Fatal(err)
	}
	m.options.Media.LLMImageMaxLength = 32
	m.options.Media.LLMImageCompressionThresholdBytes = 16 * 1024
	item, err := m.ImportBytes(t.Context(), input.Bytes(), share.Input{Name: "folder%252Fname.png", MIMEType: "application/octet-stream"}, share.ImportLimits{})
	if err != nil || item.MIMEType != "image/jpeg" || item.Name != "folder%2Fname.jpg" {
		t.Fatalf("compressed = %#v %v", item, err)
	}
	data, _, err := m.Read(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if len(data) >= 16*1024 || bounds.Dx() != 31 || bounds.Dy() != 15 {
		t.Fatalf("compressed size=%d bounds=%v", len(data), bounds)
	}
	white := color.NRGBAModel.Convert(decoded.At(bounds.Max.X-2, bounds.Max.Y/2)).(color.NRGBA)
	if white.R < 240 || white.G < 240 || white.B < 240 {
		t.Fatalf("background = %#v", white)
	}
	if info, err := m.ValidateImage(t.Context(), item.ID); err != nil || info != item {
		t.Fatalf("validation = %#v %v", info, err)
	}
	invalid, err := m.ImportBytes(t.Context(), []byte("invalid image"), share.Input{MIMEType: "image/png"}, share.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ValidateImage(t.Context(), invalid.ID); err == nil {
		t.Fatal("invalid image accepted")
	}
	text := importTestMedia(t, m, "text")
	if _, err := m.ValidateImage(t.Context(), text.ID); err == nil {
		t.Fatal("text accepted")
	}
	if _, err := compressImage(input.Bytes(), 2, 2); err == nil {
		t.Fatal("impossible compression accepted")
	}
}

func TestSourceSanitization(t *testing.T) {
	for _, raw := range []string{"https://host/path?token=x", "https://user:pass@host/file", "https://host/bot123:secret/file", "https://host/bot123%3Asecret/file", "file:///tmp/a", "https://host/%xx", "https://host/file#secret"} {
		if got := sanitizeSourceURL(raw); got != "" {
			t.Errorf("unsafe URL %q -> %q", raw, got)
		}
	}
	if got := sanitizeSourceURL(" https://host/file "); got != "https://host/file" {
		t.Fatalf("safe source = %q", got)
	}
	for _, id := range []string{"/tmp/a", `C:\private\file`, "data:text/plain,x", "base64:abc", "file:relative", "https:relative", "C:relative"} {
		if got := sanitizeSourceFileID(id); got != "" {
			t.Errorf("unsafe file ID %q -> %q", id, got)
		}
	}
	if got := sanitizeSourceFileID(" platform-id "); got != "platform-id" {
		t.Fatalf("file ID = %q", got)
	}
}
