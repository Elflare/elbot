package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appConfigFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"app.toml": fmt.Sprintf("[storage]\nsessions_sqlite_path = %q\n[sandbox]\nroot = %q\n[file_delivery]\nbackend = \"base64\"\n[runtime]\nlog_level = \"debug\"\n", filepath.Join(root, "sessions.db"), filepath.Join(root, "sandbox")),
		"providers.toml": `[providers.chat]
base_url = "https://unused.invalid/v1"
api_mode = "chat"
models = ["m"]
`,
		"state.toml": `[mode_models.work]
provider = "chat"
model = "m"
[mode_models.chat]
provider = "chat"
model = "m"
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "app.toml")
}

func cleanupAppResources(t *testing.T, r *sharedResources) {
	t.Helper()
	t.Cleanup(func() {
		if r.models != nil {
			if err := r.models.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
		if r.store != nil {
			if err := r.store.Close(); err != nil {
				t.Error(err)
			}
		}
		if r.logs != nil {
			if err := r.logs.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
}

func TestPrepareResourcesReturnsPartialOwnership(t *testing.T) {
	path := appConfigFixture(t)
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "sessions.db"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	if err == nil || !strings.Contains(err.Error(), "prepare storage") {
		t.Fatalf("error=%v", err)
	}
	if r.config == nil || r.logs == nil || r.store != nil || r.media != nil || r.models != nil {
		t.Fatalf("partial=%#v", r)
	}
	if _, err := os.Stat(r.logs.LogDir()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareResourcesCanceledBeforeAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := prepareResources(ctx, "unused")
	if !errors.Is(err, context.Canceled) || r.logs != nil || r.store != nil {
		t.Fatalf("resources=%#v error=%v", r, err)
	}
}

func TestPrepareEnvironmentUsesResolvedPathAndValidatesMode(t *testing.T) {
	path := appConfigFixture(t)
	t.Setenv("ELBOT_CONFIG_FILE", path)
	for _, mode := range []RunMode{RunModeTUI, RunModeService} {
		got, lock, err := prepareEnvironment("", mode)
		if lock != nil {
			t.Cleanup(func() {
				if err := lock.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		if err != nil || got != path {
			t.Fatalf("mode=%s path=%s lock=%v error=%v", mode, got, lock, err)
		}
	}
	if _, _, err := prepareEnvironment(path, "bad-mode"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
