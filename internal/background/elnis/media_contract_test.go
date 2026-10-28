package elnis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/delivery"
	"elbot/internal/media"
)

type fakeEventMedia struct {
	data           map[string][]byte
	inputs         []media.Input
	limits         []media.ImportLimits
	references     [][3]string
	materializeErr error
	referenceErr   error
}

func enableMedia(t *testing.T, s *Service) {
	t.Helper()
	s.media = &fakeEventMedia{data: map[string][]byte{}}
}
func testMedia(s *Service) *fakeEventMedia { return s.media.(*fakeEventMedia) }
func (m *fakeEventMedia) importBytes(ctx context.Context, data []byte, input media.Input) (string, error) {
	info, err := m.ImportBytes(ctx, data, input, media.ImportLimits{})
	return info.ID, err
}
func (m *fakeEventMedia) ImportBytes(ctx context.Context, data []byte, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return m.ImportReader(ctx, bytes.NewReader(data), int64(len(data)), input, limits)
}
func (m *fakeEventMedia) ImportReader(ctx context.Context, reader io.Reader, size int64, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	m.limits = append(m.limits, limits)
	if m.materializeErr != nil {
		return media.MediaInfo{}, m.materializeErr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return media.MediaInfo{}, err
	}
	if int64(len(data)) != size {
		return media.MediaInfo{}, fmt.Errorf("wrong file size")
	}
	id := fmt.Sprintf("media:%x", sha256.Sum256(data))
	m.data[id] = data
	m.inputs = append(m.inputs, input)
	return media.MediaInfo{ID: id, Name: input.Name, MIMEType: input.MIMEType, Size: size}, nil
}
func (m *fakeEventMedia) ImportURL(ctx context.Context, url string, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return m.ImportBytes(ctx, []byte(url), input, limits)
}
func (m *fakeEventMedia) Info(ctx context.Context, id string) (media.MediaInfo, error) {
	data, ok := m.data[id]
	if !ok {
		return media.MediaInfo{}, fmt.Errorf("missing media %s", id)
	}
	return media.MediaInfo{ID: id, Size: int64(len(data))}, nil
}
func (m *fakeEventMedia) AddEventReference(ctx context.Context, id, event, purpose string) error {
	m.references = append(m.references, [3]string{id, event, purpose})
	if m.referenceErr != nil {
		return m.referenceErr
	}
	if _, ok := m.data[id]; !ok {
		return fmt.Errorf("missing media %s", id)
	}
	return nil
}

func TestDirectMediaPassesPerCallLimits(t *testing.T) {
	s, close := newTestService(t, nil)
	defer close()
	s.cfg.Segment.MaxFileBytes = 3
	s.cfg.Segment.DownloadTimeoutSecs = 2
	req := testRequest(ModeDirect)
	req.Segments = []Segment{{Kind: SegmentKindFile, URL: "https://example.test/file", Name: "file.txt"}}
	if _, err := s.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	m := testMedia(s)
	if !reflect.DeepEqual(m.limits, []media.ImportLimits{{MaxImportBytes: 3, DownloadTimeout: 2 * time.Second}}) {
		t.Fatal(m.limits)
	}
	if len(m.references) != 1 || m.references[0][2] != "input" {
		t.Fatal(m.references)
	}
}

func TestRecordAndNoTargetDoNotMaterialize(t *testing.T) {
	s, close := newTestService(t, nil)
	defer close()
	req := testRequest(ModeRecord)
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: "https://example.test/image"}}
	if _, err := s.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	_, _ = s.runDirect(t.Context(), Event{Request: Request{Segments: req.Segments}, ResolvedTargets: "[]"}, "synthetic")
	req.Mode = ModeLLM
	req.ID = "queued"
	if _, err := s.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handle(t.Context(), "invalid-token", req); err == nil {
		t.Fatal("accepted invalid token")
	}
	if len(testMedia(s).limits) != 0 {
		t.Fatal("media eagerly materialized")
	}
	req.Mode = ModeDirect
	req.ID = "direct"
	if _, err := s.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Handle(t.Context(), "secret", req)
	if err != nil || !duplicate.Duplicate || len(testMedia(s).limits) != 1 {
		t.Fatal(duplicate, err, testMedia(s).limits)
	}
}

func TestMediaFailureMarksEventFailed(t *testing.T) {
	for _, stage := range []string{"materialize", "reference"} {
		t.Run(stage, func(t *testing.T) {
			s, close := newTestService(t, nil)
			defer close()
			failure := errors.New(stage + " unavailable")
			if stage == "materialize" {
				testMedia(s).materializeErr = failure
			} else {
				testMedia(s).referenceErr = failure
			}
			req := testRequest(ModeDirect)
			req.Segments = []Segment{{Kind: SegmentKindFile, URL: "https://example.test/file"}}
			if _, err := s.Handle(t.Context(), "secret", req); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			row, err := testRepo(s).snapshotByKey(t.Context(), req.Elwisp.Name, req.Source, req.ID)
			if err != nil || row.Status != StatusFailed || !strings.Contains(row.Error, failure.Error()) {
				t.Fatal(row, err)
			}
		})
	}
}

func TestReportFileBoundary(t *testing.T) {
	s, close := newTestService(t, nil)
	defer close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	importFile := func(path string) error {
		raw, _ := json.Marshal(map[string]any{"report_segments": []map[string]string{{"type": "image", "url": path, "mime_type": "image/png"}}})
		parsed, err := background.ParseJSONResult(string(raw))
		if err != nil {
			return err
		}
		_, err = s.importReportSegments(t.Context(), parsed.ReportSegments, "event", dir)
		return err
	}
	if err := importFile("input.png"); err != nil {
		t.Fatal(err)
	}
	m := testMedia(s)
	if len(m.inputs) != 1 || m.inputs[0].Name != "input.png" || m.inputs[0].MIMEType != "image/png" {
		t.Fatal(m.inputs)
	}
	if len(m.references) != 1 || m.references[0][1] != "event" || m.references[0][2] != "report" {
		t.Fatal(m.references)
	}
	for _, path := range []string{"../outside", `..\outside`, "/absolute", `C:\outside`, "~", "."} {
		if err := importFile(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := importFile("link/secret"); err == nil {
		t.Fatal("symlink escaped root")
	}
	if len(m.inputs) != 1 {
		t.Fatalf("invalid path reached importer: %#v", m.inputs)
	}
}

func TestExistingReportMediaRequiresExistingObject(t *testing.T) {
	s, close := newTestService(t, nil)
	defer close()
	id := "media:" + strings.Repeat("a", 64)
	_, err := s.importReportSegments(t.Context(), []delivery.Output{{Kind: delivery.KindFile, Source: delivery.Source{MediaID: id}}}, "event", t.TempDir())
	if err == nil || len(testMedia(s).references) != 0 {
		t.Fatalf("missing reference validation: %v", err)
	}
}
