package skill

import "elbot/internal/security"
import "elbot/internal/processenv"

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolregistry "elbot/internal/tool/registry"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFinalizeElSkillTextOnlyReloads(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "textonly", "#skill textonly - Text.\n** risk low\n<- $text:str!\n-> $result:str\n")
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	finalizer := NewFinalizeElSkillTool(NewManager(root, registry, nil, processenv.Environment{}))

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: []byte(`{"name":"textonly"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Go source: not found") || !strings.Contains(result.Content, "reload: ok") {
		t.Fatalf("result = %q", result.Content)
	}
	if registered, ok := registry.Get("textonly"); !ok || registered.Info().Risk != toolshare.RiskLow {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestFinalizeElSkillReturnsElyphWarnings(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "warn_finalize", "#skill warn_finalize - Text.\n** 清单：\n")
	finalizer := NewFinalizeElSkillTool(NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: []byte(`{"name":"warn_finalize"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ELyph: ok") || !strings.Contains(result.Content, "ELyph warnings:") || !strings.Contains(result.Content, "line 2:") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestFinalizeElSkillFormatsBuildsAndReloads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestGoSkill(t, root, "finalize", "package main\nfunc main(){println(\"ok\")}\n")
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	finalizer := NewFinalizeElSkillTool(NewManager(root, registry, nil, processenv.Environment{}))

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: []byte(`{"name":"finalize"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "gofmt: changed") || !strings.Contains(result.Content, "go build: ok") || !strings.Contains(result.Content, "reload: ok") {
		t.Fatalf("result = %q", result.Content)
	}
	if got := readTestGoSource(t, root, "finalize"); got != "package main\n\nfunc main() { println(\"ok\") }\n" {
		t.Fatalf("formatted source = %q", got)
	}
	binary := filepath.Join(root, "go", "finalize", "finalize")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary missing: %v", err)
	}
	if registered, ok := registry.Get("finalize"); !ok || registered.Info().Source != toolshare.SourceSkillGo {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestFinalizeElSkillReturnsBuildFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestGoSkill(t, root, "broken", "package main\n\nfunc main() { missing() }\n")
	finalizer := NewFinalizeElSkillTool(NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: []byte(`{"name":"broken"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "go build: failed") || !strings.Contains(result.Content, "stderr:") || !strings.Contains(result.Content, "missing") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestFinalizeElSkillReturnsGofmtFailure(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "badfmt", "package main\n\nfunc main( {\n")
	finalizer := NewFinalizeElSkillTool(NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: []byte(`{"name":"badfmt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "gofmt: failed") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestFinalizeElSkillReturnsElyphFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go", "badelyph")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.elyph"), []byte("missing header\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finalizer := NewFinalizeElSkillTool(NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))
	args, _ := json.Marshal(map[string]string{"name": "badelyph"})

	result, _, err := callTool(context.Background(), finalizer, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ELyph: failed") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestPreparedFinalizeRejectsChangedSourcesBeforeBuild(t *testing.T) {
	for _, scenario := range []string{"elyph", "source", "new-source"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			name := "snapshot"
			if scenario == "new-source" {
				writeTestSkill(t, root, name, "#skill snapshot - Text.\n** risk low\n")
			} else {
				writeTestGoSkill(t, root, name, "package main\nfunc main(){println(\"approved\")}\n")
			}
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			finalizer := NewFinalizeElSkillTool(NewManager(root, registry, nil, processenv.Environment{}))
			prepared, err := finalizer.PrepareCall(t.Context(), toolshare.CallRequest{Arguments: []byte(`{"name":"snapshot"}`)})
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "go", name)
			source := filepath.Join(dir, "main.go")
			if scenario != "new-source" && readTestGoSource(t, root, name) != "package main\nfunc main(){println(\"approved\")}\n" {
				t.Fatal("prepare formatted source on disk")
			}
			path, content := source, "package main\nfunc main(){println(\"changed\")}\n"
			if scenario == "elyph" {
				path, content = filepath.Join(dir, "SKILL.elyph"), "#skill snapshot - Changed.\n** risk high\n"
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := prepared.Call(t.Context()); err == nil {
				t.Fatal("accepted changed source")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content {
				t.Fatalf("changed file overwritten: %q, %v", data, err)
			}
			if _, ok := registry.Get(name); ok {
				t.Fatal("rejected finalize reloaded skill")
			}
			binary := name
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			if _, err := os.Stat(filepath.Join(dir, binary)); !os.IsNotExist(err) {
				t.Fatalf("rejected finalize built binary: %v", err)
			}
		})
	}
}
