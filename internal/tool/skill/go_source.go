package skill

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"elbot/internal/config"
)

const goBinaryEnv = "ELBOT_GO_BINARY"

func normalizeGoSource(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimRight(text, "\n") + "\n"
}

func formatGoSource(source string) (string, error) {
	formatted, err := format.Source([]byte(normalizeGoSource(source)))
	if err != nil {
		return "", fmt.Errorf("gofmt source: %w", err)
	}
	return string(formatted), nil
}

func validateGoMainSource(source string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("parse Go source: %w", err)
	}
	if file.Name == nil || file.Name.Name != "main" {
		return fmt.Errorf("go source must declare package main")
	}
	return nil
}

func resolveGoExecutable(skillRoot string) (string, error) {
	configDir := goSkillConfigDir(skillRoot)
	if value, ok, err := config.ConfigEnv(goBinaryEnv, configDir); err != nil {
		return "", err
	} else if ok {
		return validateGoExecutable(value, goBinaryEnv)
	}
	if goroot := strings.TrimSpace(os.Getenv("GOROOT")); goroot != "" {
		candidate := filepath.Join(goroot, "bin", executableName("go"))
		if path, ok := existingExecutable(candidate); ok {
			return path, nil
		}
		return "", fmt.Errorf("GOROOT is set but go executable is unavailable at %q; set %s=/path/to/go in system environment or config .env", candidate, goBinaryEnv)
	}
	if goPath, err := exec.LookPath("go"); err == nil {
		return goPath, nil
	}
	return "", fmt.Errorf("go executable not found in ElBot service PATH; set %s=/path/to/go in system environment or config .env, or configure service PATH/GOROOT, then restart ElBot", goBinaryEnv)
}

func validateGoExecutable(path, source string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s is empty", source)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s points to unavailable go executable %q: %w", source, path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s points to a directory, not go executable: %q", source, path)
	}
	return path, nil
}

func goSkillConfigDir(skillRoot string) string {
	root := filepath.Clean(strings.TrimSpace(skillRoot))
	if filepath.Base(root) == "skills" {
		return filepath.Dir(root)
	}
	parent := filepath.Dir(root)
	if filepath.Base(parent) == "go" && filepath.Base(filepath.Dir(parent)) == "skills" {
		return filepath.Dir(filepath.Dir(parent))
	}
	return filepath.Dir(root)
}

func existingExecutable(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

type goBuildResult struct {
	Stdout string
	Stderr string
	Err    error
}

func buildGoSkill(ctx context.Context, root, name string, timeoutMS int) error {
	result, err := runGoBuild(ctx, root, name, timeoutMS)
	if err != nil {
		return err
	}
	if result.Err != nil {
		return fmt.Errorf("go build failed: %w\nstdout:\n%s\nstderr:\n%s", result.Err, truncateOutput(result.Stdout), truncateOutput(result.Stderr))
	}
	return nil
}

func runGoBuild(ctx context.Context, root, name string, timeoutMS int) (goBuildResult, error) {
	goPath, err := resolveGoExecutable(root)
	if err != nil {
		return goBuildResult{}, err
	}
	binary := name
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	timeout := 60 * time.Second
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, goPath, "build", "-o", binary, ".")
	cmd.Dir = root
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return goBuildResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}, nil
}
