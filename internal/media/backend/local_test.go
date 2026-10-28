package backend

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/media/share"
)

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestLocalBackendRoundTrip(t *testing.T) {
	b := &LocalBackend{Root: t.TempDir()}
	hash := strings.Repeat("ab", 32)
	data := "local media content"
	want := filepath.Join(b.Root, hash[:2], hash[2:])
	for range 2 {
		location, err := b.Put(t.Context(), share.IDPrefix+hash, strings.NewReader(data), int64(len(data)), "text/plain")
		if err != nil || location != want {
			t.Fatalf("Put = %q, %v; want %q", location, err, want)
		}
	}
	reader, err := b.Open(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || string(got) != data {
		t.Fatalf("read = %q, %v; close = %v", got, err, closeErr)
	}
	for range 2 {
		if err := b.Remove(t.Context(), want); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Open(t.Context(), want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open removed object = %v", err)
	}
	assertNoLocalTemps(t, b.Root)
}

func TestLocalBackendUsesSuppliedLocation(t *testing.T) {
	b := &LocalBackend{Root: t.TempDir()}
	location := filepath.Join(t.TempDir(), "selected-object")
	if err := os.WriteFile(location, []byte("selected"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := b.Open(t.Context(), location)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || string(got) != "selected" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := b.Remove(t.Context(), location); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(location); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected location remains: %v", err)
	}
}

func TestLocalBackendPutFailureCleansTemporaryFile(t *testing.T) {
	for _, stage := range []string{"read", "rename", "cancel before commit"} {
		t.Run(stage, func(t *testing.T) {
			b := &LocalBackend{Root: t.TempDir()}
			id := share.IDPrefix + strings.Repeat("a", 64)
			location := b.path(id)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("read failed")
			var input io.Reader = strings.NewReader("content")
			switch stage {
			case "read":
				input = readerFunc(func(p []byte) (int, error) { return copy(p, "partial"), failure })
			case "rename":
				// An existing directory must not turn a failed rename into success.
				if err := os.MkdirAll(location, 0o755); err != nil {
					t.Fatal(err)
				}
			case "cancel before commit":
				input = readerFunc(func(p []byte) (int, error) {
					cancel()
					return copy(p, "content"), io.EOF
				})
			}
			got, err := b.Put(ctx, id, input, 7, "text/plain")
			if err == nil || got != "" {
				t.Fatalf("Put = %q, %v", got, err)
			}
			if stage == "read" && !errors.Is(err, failure) {
				t.Fatalf("lost reader error: %v", err)
			}
			if stage == "cancel before commit" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if stage != "rename" {
				if _, err := os.Stat(location); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed write published object: %v", err)
				}
			}
			assertNoLocalTemps(t, b.Root)
		})
	}
}

func TestLocalBackendCanceledOperationsHaveNoSideEffects(t *testing.T) {
	b := &LocalBackend{Root: filepath.Join(t.TempDir(), "objects")}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	id := share.IDPrefix + strings.Repeat("b", 64)
	input := readerFunc(func([]byte) (int, error) { t.Error("read after cancellation"); return 0, io.EOF })
	if _, err := b.Put(ctx, id, input, 0, "text/plain"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v", err)
	}
	if _, err := os.Stat(b.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Put created directory: %v", err)
	}
	location := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(location, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(ctx, location); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open = %v", err)
	}
	if err := b.Remove(ctx, location); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := os.Stat(location); err != nil {
		t.Fatalf("canceled Remove changed object: %v", err)
	}
}

func assertNoLocalTemps(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "*", ".media-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}
