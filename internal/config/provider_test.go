package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProvidersUsesSelectedFileAndReloadsConfigEnv(t *testing.T) {
	dir, external := t.TempDir(), t.TempDir()
	path := filepath.Join(external, "custom-providers.toml")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "app.toml"), "invalid main config = [")
	write(filepath.Join(dir, "state.toml"), "invalid state = [")
	write(path, "[providers.p]\napi_key_env='ELBOT_RELOAD_TEST_KEY'\nmodels=['m']\n[providers.p.model_configs.m]\ncontext_window=123\n[model_metadata]\ndefault_context_window=456\n")
	t.Setenv("ELBOT_RELOAD_TEST_KEY", "")
	for _, key := range []string{"first", "second"} {
		write(filepath.Join(dir, ".env"), "ELBOT_RELOAD_TEST_KEY="+key)
		file, err := LoadProviders(path, dir)
		if err != nil {
			t.Fatal(err)
		}
		provider := file.Providers["p"]
		if provider.APIKey != key || provider.APIMode != "chat" || provider.ModelConfigs["m"].ContextWindow != 123 || file.ModelMetadata.DefaultContextWindow != 456 {
			t.Fatal("provider file or dotenv was not reloaded")
		}
	}
	t.Setenv("ELBOT_RELOAD_TEST_KEY", "process")
	file, err := LoadProviders(path, dir)
	if err != nil || file.Providers["p"].APIKey != "process" {
		t.Fatal("process environment precedence changed", err)
	}
	for _, invalid := range []string{"[", "[providers.p]\napi_mode='invalid'"} {
		write(path, invalid)
		if _, err := LoadProviders(path, dir); err == nil {
			t.Fatal("invalid provider file accepted")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProviders(path, dir); err == nil {
		t.Fatal("missing provider file accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing provider file was recreated")
	}
}

func TestResponsesStoreValidationAtStartupAndReload(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		invalid       bool
	}{
		{"provider_false", "[providers.p.extra_payload]\nstore=false\n", false},
		{"model_true", "[providers.p.model_configs.m.extra_payload]\nstore=true\n", false},
		{"provider_string", "[providers.p.extra_payload]\nstore='false'\n", true},
		{"model_number", "[providers.p.model_configs.m.extra_payload]\nstore=1\n", true},
		{"duplicate", "[providers.p.extra_payload]\nstore=false\n[providers.p.model_configs.m.extra_payload]\nstore=false\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "app.toml")
			provider := filepath.Join(dir, "providers.toml")
			if err := os.WriteFile(main, []byte("[mode_models.work]\nprovider='p'\nmodel='m'\n[mode_models.chat]\nprovider='p'\nmodel='m'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(provider, []byte("[providers.p]\napi_mode='response'\nmodels=['m']\n"+test.payload), 0600); err != nil {
				t.Fatal(err)
			}
			_, startupErr := Load(main)
			_, reloadErr := LoadProviders(provider, dir)
			for _, err := range []error{startupErr, reloadErr} {
				if (err != nil) != test.invalid || err != nil && !strings.Contains(err.Error(), "store") {
					t.Fatalf("startup=%v reload=%v", startupErr, reloadErr)
				}
			}
		})
	}
}

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
