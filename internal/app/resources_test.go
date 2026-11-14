package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
)

func appConfigFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"app.toml": fmt.Sprintf("[storage]\nsessions_sqlite_path = %q\nchat_history_sqlite_path = %q\n[sandbox]\nroot = %q\n[file_delivery]\nbackend = \"base64\"\n[runtime]\nlog_level = \"debug\"\n", filepath.Join(root, "sessions.db"), filepath.Join(root, "history.db"), filepath.Join(root, "sandbox")),
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
		if err := r.models.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := r.media.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := r.historyStore.Close(); err != nil {
			t.Error(err)
		}
		if err := r.store.Close(); err != nil {
			t.Error(err)
		}
		if err := r.logs.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func TestPrepareResourcesConstructsConfiguredModels(t *testing.T) {
	path := appConfigFixture(t)
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	if err != nil {
		t.Fatal(err)
	}
	selected := r.models.ResolveMode("work")
	if selected.Client == nil || selected.Provider != "chat" || selected.Model != "m" {
		t.Fatalf("selection=%+v", selected)
	}
}

func TestPrepareResourcesReconcilesMediaBeforeBusinessWiring(t *testing.T) {
	path := appConfigFixture(t)
	store, err := sqlite.New(t.Context(), filepath.Join(filepath.Dir(path), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	cfg.Sandbox.Root = filepath.Join(filepath.Dir(path), "sandbox")
	cfg.FileDelivery.Backend = "base64"
	seed, err := media.NewMediaCenter(t.Context(), cfg, media.MediaCenterDeps{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close(context.Background())
	item, err := seed.ImportBytes(t.Context(), []byte("history media"), media.Input{Name: "file.txt", MIMEType: "text/plain"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AssociateHistory(t.Context(), media.HistoryMessage{
		ID: "deleted", Platform: "test", ChatID: "scope", MessageID: "message",
	}, 1, "file", item.Data.ID); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	if err != nil {
		t.Fatal(err)
	}
	items, err := r.store.Media().ListHistory(t.Context(), "", 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("startup retained stale history associations: %v, %v", items, err)
	}
}

func TestBuildBusinessLinesConnectsProductionSupervisors(t *testing.T) {
	r, err := prepareResources(t.Context(), appConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := buildBusinessLines(t.Context(), r, RunModeService, &runtimeControl{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shutdown(context.Background(), lines, r, &testConfigLock{}); err != nil {
			t.Error(err)
		}
	})
	if len(lines) != 5 {
		t.Fatalf("business lines=%v", lines)
	}
	lines[componentPlatform].supervisor.BeginClose()
	if err := lines[componentPlatform].waitIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
}
