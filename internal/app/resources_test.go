package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/platform"
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
		if r.models != nil {
			if err := r.models.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
		if r.media != nil {
			if err := r.media.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
		if r.historyStore != nil {
			if err := r.historyStore.Close(); err != nil {
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

func TestPrepareResourcesReturnsStoreWhenHistoryConstructionFails(t *testing.T) {
	path := appConfigFixture(t)
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "history.db"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	requireLifecycleError(t, err, componentChatHistory, phaseBuild)
	if r.store == nil || r.historyStore != nil || r.media != nil {
		t.Fatalf("resources=%+v err=%v", r, err)
	}
}

func TestPrepareResourcesReturnsPartialOwnership(t *testing.T) {
	path := appConfigFixture(t)
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "sessions.db"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	requireLifecycleError(t, err, componentStorage, phaseBuild)
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

func TestPrepareResourcesRetainsPartialModelsForCleanup(t *testing.T) {
	path := appConfigFixture(t)
	providersPath := filepath.Join(filepath.Dir(path), "providers.toml")
	file, err := os.OpenFile(providersPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("proxy = '://invalid'\n")
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
	r, err := prepareResources(t.Context(), path)
	cleanupAppResources(t, r)
	requireLifecycleError(t, err, componentModels, phaseBuild)
	if r.models == nil || r.media == nil || r.historyStore == nil {
		t.Fatalf("resources=%+v error=%v", r, err)
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
		ID: "deleted", Platform: "test", ScopeID: "scope", MessageID: "message",
	}, 1, "file", item.ID); err != nil {
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

func TestBuildBusinessLinesPreservesPartialPlatform(t *testing.T) {
	r, err := prepareResources(t.Context(), appConfigFixture(t))
	cleanupAppResources(t, r)
	if err != nil {
		t.Fatal(err)
	}
	r.config.Platform["cli"] = map[string]any{"enabled": "invalid boolean"}
	lines, err := buildBusinessLines(t.Context(), r, RunModeService)
	t.Cleanup(func() {
		if result := stopBusinessLines(context.Background(), lines); result.err != nil {
			t.Error(result.err)
		}
	})
	requireLifecycleError(t, err, componentPlatform, phaseBuild)
	if len(lines) != 1 || lines[0].kind != componentPlatform || lines[0].supervisor == nil || lines[0].waitIngress == nil {
		t.Fatalf("incorrect partial ownership: %+v", lines)
	}
}

func TestBuildBusinessLinesRegistersProductionOrderAndIngress(t *testing.T) {
	r, err := prepareResources(t.Context(), appConfigFixture(t))
	cleanupAppResources(t, r)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := buildBusinessLines(t.Context(), r, RunModeService)
	t.Cleanup(func() {
		if result := stopBusinessLines(context.Background(), lines); result.err != nil {
			t.Error(result.err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	ordered := lines.inStartOrder()
	want := []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground}
	var got []componentKind
	for _, line := range ordered {
		got = append(got, line.kind)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("startup order=%v", got)
	}
	for i, line := range ordered {
		if line.beginOrder != []int{5, 3, 4, 1, 2}[i] || line.closeOrder != []int{4, 2, 3, 5, 1}[i] {
			t.Fatalf("shutdown registration=%+v", line)
		}
		if (line.waitIngress != nil) != (line.kind == componentPlatform) {
			t.Fatalf("ingress registration=%+v", line)
		}
		if line.kind == componentPlatform {
			if _, ok := line.supervisor.(*platform.Supervisor); !ok {
				t.Fatalf("platform supervisor=%T", line.supervisor)
			}
			line.supervisor.BeginClose()
			if err := line.waitIngress(t.Context()); err != nil {
				t.Fatalf("registered ingress wait: %v", err)
			}
		}
	}
}
