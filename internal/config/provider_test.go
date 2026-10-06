package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderAPIModeDefaultsAndValidationInLoadAndInspection(t *testing.T) {
	for _, mode := range []string{"", "chat", "response", "responses", "CHAT"} {
		t.Run("mode="+mode, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "app.toml")
			files := map[string]string{
				"app.toml":       "[mode_models.work]\nprovider='custom'\nmodel='m'\n[mode_models.chat]\nprovider='custom'\nmodel='m'\n",
				"providers.toml": "[providers.custom]\nbase_url='https://example.invalid/v1'\nmodels=['m']\n",
			}
			if mode != "" {
				files["providers.toml"] += "api_mode='" + mode + "'\n"
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			loaded, loadErr := Load(main)
			inspection, err := NewInspector().Inspect(context.Background(), main)
			if err != nil {
				t.Fatal(err)
			}
			invalid := mode == "responses" || mode == "CHAT"
			found := false
			for _, issue := range inspection.Issues {
				if issue.Level == LevelError && strings.Join(issue.Field, ".") == "providers.custom.api_mode" {
					found = true
					if issue.Path != filepath.Join(dir, "providers.toml") {
						t.Fatalf("wrong source: %+v", issue)
					}
				}
			}
			if invalid {
				if loadErr == nil || !strings.Contains(loadErr.Error(), "api_mode") || !found {
					t.Fatalf("load=%v api_mode_issue=%v", loadErr, found)
				}
				return
			}
			if loadErr != nil || found {
				t.Fatalf("load=%v api_mode_issue=%v", loadErr, found)
			}
			want := mode
			if want == "" {
				want = "chat"
			}
			if loaded.Providers["custom"].APIMode != want || inspection.Config.Providers["custom"].APIMode != want {
				t.Fatal("load and inspection disagree on defaults")
			}
		})
	}
}
