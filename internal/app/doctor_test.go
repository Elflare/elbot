package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent"
	"elbot/internal/contextinfo"
)

func TestDoctorThroughProductionWiring(t *testing.T) {
	path := appConfigFixture(t)
	root := filepath.Dir(path)
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(root, "custom.toml")
	if err := os.Rename(path, customPath); err != nil {
		t.Fatal(err)
	}
	providersPath := filepath.Join(root, "custom-providers.toml")
	if err := os.Rename(filepath.Join(root, "providers.toml"), providersPath); err != nil {
		t.Fatal(err)
	}
	main = append(main, []byte("\n[config_files]\nproviders = 'custom-providers.toml'\n[commands]\nprefixes = ['-', '/']\n")...)
	if err := os.WriteFile(customPath, main, 0600); err != nil {
		t.Fatal(err)
	}
	resources, err := prepareResources(t.Context(), customPath)
	cleanupAppResources(t, resources)
	if err != nil {
		t.Fatal(err)
	}
	// Use the real CLI output without starting terminal input or network clients.
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = stdin
		_ = input.Close()
	})
	lines, err := buildBusinessLines(t.Context(), resources, RunModeTUI)
	t.Cleanup(func() {
		if result := stopBusinessLines(context.Background(), lines); result.err != nil {
			t.Error(result.err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var supervisor *agent.Supervisor
	for _, line := range lines {
		if line.kind == componentAgent {
			supervisor = line.supervisor.(*agent.Supervisor)
		}
	}
	if supervisor == nil {
		t.Fatal("Agent was not constructed")
	}

	// Change files after assembly: diagnosis must reread disk using the selected
	// path and module schemas without reloading or starting their runtimes.
	main = append(main, []byte("\n[platform.telegram]\nenabled = 'invalid boolean'\n")...)
	if err := os.WriteFile(customPath, main, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providersPath, []byte("[providers.chat]\nbase_url = [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(root, "plugins", "hooks.toml")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte("rules = 'invalid rules'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := doctorConfigSnapshot(t, root)
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "cli:local", Role: contextinfo.RoleSuperadmin})
	for _, text := range []string{"-doctor", "/doctor"} {
		t.Run(text, func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			stdout := os.Stdout
			os.Stdout = output
			defer func() {
				os.Stdout = stdout
				_ = output.Close()
			}()
			if err := supervisor.HandleMessage(ctx, agent.MessageRequest{Text: text}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			report := string(data)
			if strings.Count(report, "主配置：") != 1 {
				t.Fatalf("expected one diagnosis report, got output=%s", report)
			}
			for _, want := range []string{"主配置：" + customPath, providersPath, hooksPath, "platform.telegram.enabled", "rules", "[错误]"} {
				if !strings.Contains(report, want) {
					t.Errorf("diagnosis missing %q:\n%s", want, report)
				}
			}
			if after := doctorConfigSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Error("diagnosis created or modified configuration files")
			}
		})
	}
}

type doctorConfigFile struct {
	Content string
	Mode    fs.FileMode
	ModTime time.Time
}

func doctorConfigSnapshot(t *testing.T, root string) map[string]doctorConfigFile {
	t.Helper()
	files := make(map[string]doctorConfigFile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".toml" && filepath.Ext(path) != ".md" && entry.Name() != ".env" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = doctorConfigFile{string(data), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
