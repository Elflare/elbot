// 执行 Skill 子进程、查找编译器并收集有界输出。
package skill

import (
	"context"
	"elbot/internal/processenv"
	toolshare "elbot/internal/tool/share"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRunnerTimeout = 30 * time.Second
	maxRunnerOutput      = 16 * 1024
	maxRunnerStdout      = 1 * 1024 * 1024
)

type limitedOutput struct {
	// buf holds only the leading limit bytes; remaining output is drained and discarded.
	buf []byte
	// limit bounds captured bytes independently for stdout and stderr.
	limit int
	// truncated reports discarded bytes, so incomplete stdout is never parsed as a result.
	truncated bool
}

func (out *limitedOutput) Write(p []byte) (int, error) {
	n := min(len(p), out.limit-len(out.buf))
	out.buf = append(out.buf, p[:n]...)
	if n < len(p) {
		out.truncated = true
	}
	return len(p), nil
}

func (out *limitedOutput) String() string {
	text := string(out.buf)
	if out.truncated {
		text += "\n... output truncated ...\n"
	}
	return text
}

func runnerTimeout(timeoutMS int) time.Duration {
	if timeoutMS > 0 {
		return time.Duration(timeoutMS) * time.Millisecond
	}
	return defaultRunnerTimeout
}

func runMediaCommand(ctx context.Context, label string, cmd *exec.Cmd, mediaCall toolshare.MediaCall) (*toolshare.Result, error) {
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		out := truncateOutput(stdout.String())
		errText := stderr.String()
		return nil, commandError(ctx, label, err, out, errText)
	}
	if stdout.truncated {
		return nil, fmt.Errorf("%s stdout exceeds %d byte limit%s", label, maxRunnerStdout, commandOutputSuffix("", stderr.String()))
	}
	return resultFromStdoutWithMedia(ctx, stdout.String(), mediaCall, cmd.Dir)
}

func commandError(ctx context.Context, label string, err error, stdout, stderr string) error {
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out: %w%s", label, errors.Join(ctxErr, err), commandOutputSuffix(stdout, stderr))
	} else if errors.Is(ctxErr, context.Canceled) {
		return fmt.Errorf("%s canceled: %w%s", label, errors.Join(ctxErr, err), commandOutputSuffix(stdout, stderr))
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s process failed with exit code %d: %w%s", label, exitErr.ExitCode(), err, commandOutputSuffix(stdout, stderr))
	}
	return fmt.Errorf("start %s failed: %w", label, err)
}

func commandOutputSuffix(stdout, stderr string) string {
	parts := []string{}
	if strings.TrimSpace(stdout) != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}

func truncateOutput(text string) string {
	if len(text) <= maxRunnerOutput {
		return text
	}
	return text[:maxRunnerOutput] + "\n... output truncated ...\n"
}

const goBinaryEnv = "ELBOT_GO_BINARY"

func resolveGoExecutable(environment processenv.Environment) (string, error) {
	if value, ok := environment.Lookup(goBinaryEnv); ok && strings.TrimSpace(value) != "" {
		return validateGoExecutable(value, goBinaryEnv)
	}
	if goroot, ok := environment.Lookup("GOROOT"); ok && strings.TrimSpace(goroot) != "" {
		candidate := filepath.Join(goroot, "bin", executableName("go"))
		if path, ok := existingExecutable(candidate); ok {
			return path, nil
		}
		return "", fmt.Errorf("GOROOT is set but go executable is unavailable at %q; set %s=/path/to/go in system environment or config .env", candidate, goBinaryEnv)
	}
	if goPath, err := environment.LookPath("go"); err == nil {
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

func runGoBuild(ctx context.Context, root, name string, timeoutMS int, environment processenv.Environment) (goBuildResult, error) {
	goPath, err := resolveGoExecutable(environment)
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
	cmd := environment.CommandContext(runCtx, goPath, "build", "-o", binary, ".")
	cmd.Dir = root
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return goBuildResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}, nil
}

func detectGoModVersion(ctx context.Context, environment processenv.Environment) (string, error) {
	goPath, err := environment.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("system go executable not found; install Go and ensure it is in PATH before creating Go skill")
	}
	cmd := environment.CommandContext(ctx, goPath, "env", "GOVERSION")
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("detect go version: %w\nstderr:\n%s", err, truncateOutput(stderr.String()))
	}
	version, err := parseGoModVersion(stdout.String())
	if err != nil {
		return "", err
	}
	return version, nil
}
