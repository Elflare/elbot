package transfer

import (
	"context"
	"elbot/internal/events"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/media/share"
	"elbot/internal/operationresult"
)

func TestOutputPreparationFailureRetainedByMedia(t *testing.T) {
	for _, stage := range []string{"mkdir", "open", "read", "close", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			business, releaseErr := errors.New("business failed"), errors.New("release failed")
			h := &fakeHeld{info: share.MediaInfo{ID: "one"}}
			h.close = func() error {
				if h.closes.Load() == 1 {
					return releaseErr
				}
				return nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &failingReader{Reader: strings.NewReader("body")}
			switch stage {
			case "mkdir":
				t.Setenv("TMPDIR", filepath.Join(root, "absent"))
			case "read":
				reader.Reader = failingRead{business}
			case "close":
				reader.closeErr = business
			}
			h.open = func(context.Context) (io.ReadCloser, error) {
				if stage == "open" {
					return nil, business
				}
				if stage == "cancel" {
					cancel()
				}
				return reader, nil
			}
			var records []events.LogRecord
			m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
				return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: h}, nil
			}}, Options{Log: func(_ context.Context, record events.LogRecord) { records = append(records, record) }})
			result, usageID, err := m.ResolveForOutput(ctx, "one")
			if stage == "close" {
				if err != nil || result.Status != operationresult.Success || usageID == "" || len(records) != 1 || records[0].Content != business.Error() {
					t.Fatalf("result=%s err=%v logs=%+v", usageID, err, records)
				}
				m.FinishUsages([]string{usageID})
				if err := m.CleanupUsages(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			} else if usageID != "" || err == nil || errors.Is(err, releaseErr) {
				t.Fatalf("lost usageID: %v", err)
			}
			if stage == "open" || stage == "read" {
				if !errors.Is(err, business) {
					t.Fatal(err)
				}
			}
			if stage == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if stage != "open" && stage != "mkdir" && reader.closes != 1 {
				t.Fatal("reader not closed")
			}
			if err := m.CleanupUsages(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary files leaked: %v %v", entries, err)
			}
		})
	}
}

func TestOutputRetriesDirectoryRemovalIndependently(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permissions")
	}
	h := &fakeHeld{info: share.MediaInfo{ID: "one"}}
	m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
		return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: h}, nil
	}}, Options{Log: func(context.Context, events.LogRecord) {}})
	source, usageID, err := m.ResolveForOutput(t.Context(), "one")
	if err != nil || source.Status != operationresult.Success {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	dir := filepath.Dir(source.Data.Path)
	t.Cleanup(func() { _ = os.Chmod(dir, 0700); _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	m.FinishUsages([]string{usageID})
	if err := m.CleanupUsages(t.Context(), nil); err != nil || h.closes.Load() != 1 {
		t.Fatalf("failed removal must still release: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.CleanupUsages(t.Context(), nil); err != nil || h.closes.Load() != 1 {
		t.Fatalf("repeated release: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
