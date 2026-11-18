package weave

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/tool/share"
)

func TestRunnerConcurrentCalls(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		project    bool
	}{
		{"system", "system", false}, {"uv_project", "", true}, {"auto_no_project", "", false}, {"uv_temporary", "uv", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.mode != "system" {
				if _, err := exec.LookPath("uv"); err != nil {
					t.Skip("uv unavailable")
				}
			}
			dir := t.TempDir()
			t.Setenv("UV_OFFLINE", "1")
			t.Setenv("UV_CACHE_DIR", t.TempDir())
			if test.project {
				if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='weave-test'\nversion='0.1.0'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			unblock := make(chan struct{})
			w := share.WeaveContext{Names: []string{"fetch"}, Call: func(ctx context.Context, name string, args json.RawMessage) (operationresult.Result[string], error) {
				var a struct{ I int }
				if err := json.Unmarshal(args, &a); err != nil {
					return operationresult.Result[string]{}, err
				}
				switch a.I {
				case 0:
					select {
					case <-unblock:
						return operationresult.Result[string]{Status: operationresult.Success, Data: "first"}, nil
					case <-ctx.Done():
						return operationresult.Result[string]{}, ctx.Err()
					}
				case 1:
					close(unblock)
					return operationresult.Result[string]{Status: operationresult.Success, Data: "second"}, nil
				default:
					return operationresult.Result[string]{Status: operationresult.Rejected, Message: "denied"}, nil
				}
			}}
			code := `import json, sys
a, b = await asyncio.gather(fetch(i=0), fetch(i=1))
try:
    await fetch(i=2)
except RuntimeError as exc:
    print(json.dumps([a,b,str(exc)]))
print("script stderr", file=sys.stderr)`
			got, _, err := run(share.WithWeaveContext(t.Context(), w), processenv.Environment{}, dir, args{Code: code, Mode: test.mode, Timeout: 10})
			if err != nil || got.Status != operationresult.Success || !strings.Contains(got.Data.Content, `["first", "second", "denied"]`) || !strings.Contains(got.Data.Content, "stderr:\nscript stderr") {
				t.Fatalf("result=%+v data=%+v err=%v", got, got.Data, err)
			}
			wantWarning := ""
			if test.mode == "uv" && !test.project {
				wantWarning = "已使用临时 uv 环境。"
			}
			if strings.Join(got.Data.Warnings, "\n") != wantWarning {
				t.Fatalf("warnings = %v, want %q", got.Data.Warnings, wantWarning)
			}
		})
	}
}

func TestFallbackAndTimeout(t *testing.T) {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	path, err := exec.LookPath(python)
	if err != nil {
		t.Skip("system Python unavailable")
	}
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Symlink(path, filepath.Join(dir, python)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		for mode, warning := range map[string]string{"": "", "uv": "未找到 uv，回退到系统 Python。"} {
			got, _, err := run(t.Context(), processenv.Environment{}, dir, args{Code: "print(42)", Mode: mode, Timeout: 5})
			if err != nil || got.Status != operationresult.Success || strings.Join(got.Data.Warnings, "\n") != warning {
				t.Fatalf("fallback: %+v %v", got, err)
			}
		}
		for mode, reason := range map[string]string{"": "未找到 uv", "uv": "未找到 uv", "system": `改用 mode="uv"。`} {
			got, _, err := run(t.Context(), processenv.Environment{}, dir, args{Code: "raise AssertionError()", Mode: mode, Packages: []string{"sample"}, Timeout: 5})
			if err != nil || got.Status != operationresult.InvalidArguments || got.Message != "packages 仅限 uv。"+reason {
				t.Fatalf("mode=%q packages: %+v %v", mode, got, err)
			}
		}
	}
	stopped := make(chan struct{})
	w := share.WeaveContext{Names: []string{"wait"}, Call: func(ctx context.Context, _ string, _ json.RawMessage) (operationresult.Result[string], error) {
		<-ctx.Done()
		close(stopped)
		return operationresult.Result[string]{}, ctx.Err()
	}}
	got, _, _ := run(share.WithWeaveContext(t.Context(), w), processenv.Environment{}, dir, args{Code: "await wait()", Mode: "system", Timeout: 1})
	if got.Status != operationresult.Timeout {
		t.Fatalf("timeout: %+v", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("child did not stop")
	}
}

func TestUVPreparationFailureAndMissingPython(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX uv fixture")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("requires system Python")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uv"), []byte("#!/bin/sh\ncase \"$1\" in workspace) exit 0;; esac\necho 'uv setup failed' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	a := args{Mode: "uv", Code: "print(42)", Timeout: 5}
	_, _, err = run(t.Context(), processenv.Environment{}, dir, a)
	if err == nil || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("missing system Python: %v", err)
	}
	if err := os.Symlink(python, filepath.Join(dir, "python3")); err != nil {
		t.Fatal(err)
	}
	a.Mode = "" // 自动模式仍报告真实的 uv 准备失败。
	got, _, err := run(t.Context(), processenv.Environment{}, dir, a)
	if err != nil || got.Status != operationresult.Success || strings.TrimSpace(got.Data.Content) != "42" || !strings.Contains(strings.Join(got.Data.Warnings, " "), "uv setup failed") {
		t.Fatalf("setup fallback: %+v %v", got, err)
	}
	a.Packages = []string{"sample"}
	got, _, err = run(t.Context(), processenv.Environment{}, dir, a)
	if err != nil || got.Status != operationresult.InvalidArguments || got.Message != "packages 仅限 uv。uv 环境准备失败：uv setup failed" {
		t.Fatalf("setup failure with packages: %+v %v", got, err)
	}
}

func TestTemporaryPackageLeavesProjectDependenciesAlone(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv unavailable")
	}
	dir := t.TempDir()
	manifest := "[project]\nname='weave-fixture'\nversion='1.0'\n"
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	wheel := filepath.Join(t.TempDir(), "example_weave-1.0-py3-none-any.whl")
	file, err := os.Create(wheel)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for path, content := range map[string]string{
		"example_weave.py":                     "value = 42\n",
		"example_weave-1.0.dist-info/WHEEL":    "Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
		"example_weave-1.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: example-weave\nVersion: 1.0\n",
		"example_weave-1.0.dist-info/RECORD":   "",
	} {
		entry, err := archive.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	t.Setenv("UV_OFFLINE", "1")
	a := args{Code: "import example_weave; print(example_weave.value)", Packages: []string{wheel}, Timeout: 10}
	got, _, err := run(t.Context(), processenv.Environment{}, dir, a)
	if err != nil || got.Status != operationresult.Success || strings.TrimSpace(got.Data.Content) != "42" {
		t.Fatalf("package run: %+v %v", got, err)
	}
	// The same package also works outside a project with explicit uv.
	plain := t.TempDir()
	got, _, err = run(t.Context(), processenv.Environment{}, plain, a)
	if err != nil || got.Status != operationresult.InvalidArguments || !strings.Contains(got.Message, `未检测到 uv 项目，改用 mode="uv"`) {
		t.Fatalf("auto packages outside project: %+v %v", got, err)
	}
	a.Mode = "uv"
	got, _, err = run(t.Context(), processenv.Environment{}, plain, a)
	if err != nil || got.Status != operationresult.Success || strings.TrimSpace(got.Data.Content) != "42" || !strings.Contains(strings.Join(got.Data.Warnings, " "), "临时 uv 环境") {
		t.Fatalf("isolated package: %+v %v", got, err)
	}
	a.Packages = nil
	a.Code = "with open('count', 'a') as f: f.write('1')\nraise RuntimeError('from script')"
	got, _, err = run(t.Context(), processenv.Environment{}, plain, a)
	count, readErr := os.ReadFile(filepath.Join(plain, "count"))
	if err != nil || got.Status != operationresult.Failed || !strings.Contains(got.Message, "from script") || readErr != nil || string(count) != "1" {
		t.Fatalf("failed script: %+v attempts=%s err=%v read=%v", got, count, err, readErr)
	}
	content, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil || string(content) != manifest {
		t.Fatalf("changed manifest: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected lockfile: %v", err)
	}
}
