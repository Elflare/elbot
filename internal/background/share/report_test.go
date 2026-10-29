package share

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/workspace"
)

func TestBuildReportSegmentOutputKeepsHTTPMediaURL(t *testing.T) {
	out, err := BuildReportSegmentOutput(t.Context(), delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/chart.png"}, Name: "chart.png"}, workspace.PathScope{}, nil, media.ImportLimits{})
	if err != nil || out.Kind != delivery.KindImage || out.Source.URL != "https://example.com/chart.png" {
		t.Fatalf("output=%#v err=%v", out, err)
	}
}

func TestBuildReportSegmentOutputReadsWithinTaskScope(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"inside": "report.txt", "outside": filepath.Join(outside, "secret.txt"), "outside-dir": outside} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path string
		ok   bool
	}{{"report.txt", true}, {"inside", true}, {"outside", false}, {"outside-dir/secret.txt", false}, {".", false}} {
		t.Run(tc.path, func(t *testing.T) {
			calls := 0
			var opened *os.File
			importer := reportMediaFunc(func(_ context.Context, reader io.Reader, size int64, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
				calls++
				opened = reader.(*os.File)
				data, err := io.ReadAll(reader)
				if err != nil || string(data) != "report" || size != 6 || limits.MaxImportBytes != 10 {
					t.Fatalf("read=%q size=%d limits=%+v err=%v", data, size, limits, err)
				}
				return media.MediaInfo{ID: "media:report", Name: input.Name, MIMEType: "text/plain"}, nil
			})
			out, err := BuildReportSegmentOutput(t.Context(), delivery.Output{Kind: delivery.KindFile, Source: delivery.Source{Path: tc.path}}, workspace.PathScope{Dir: dir, Restricted: true}, importer, media.ImportLimits{MaxImportBytes: 10})
			if !tc.ok {
				if err == nil || calls != 0 {
					t.Fatalf("unsafe import: calls=%d err=%v", calls, err)
				}
				return
			}
			if err != nil || calls != 1 || out.Source.MediaID != "media:report" || out.Source.Path != "" || out.Name != filepath.Base(tc.path) {
				t.Fatalf("output=%+v calls=%d err=%v", out, calls, err)
			}
			if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("report file was not closed")
			}
		})
	}
}

type reportMediaFunc func(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)

func (f reportMediaFunc) ImportReader(ctx context.Context, reader io.Reader, size int64, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return f(ctx, reader, size, input, limits)
}
