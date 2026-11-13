// 执行 Skill 子进程、查找编译器并收集有界输出。
package skill

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	toolshare "elbot/internal/tool/share"
	"errors"
	"fmt"
	"log/slog"
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

func runMediaCommand(ctx context.Context, label string, cmd *exec.Cmd, mediaCall toolshare.MediaCall) (operationresult.Result[*toolshare.Result], error) {
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cancel := cmd.Cancel
	cancelRequested := make(chan error, 1)
	cmd.Cancel = func() error {
		err := cancel()
		cancelRequested <- err
		return err
	}
	err := cmd.Run()
	select {
	case cancelErr := <-cancelRequested:
		if cancelErr != nil {
			if !errors.Is(cancelErr, os.ErrProcessDone) && !errors.Is(err, cancelErr) {
				events.AsyncLog(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn,
					EventName: "skill_process_cleanup_failed", Module: "skill", Content: cancelErr.Error(),
					Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(cancelErr)})
			}
		} else if processenv.ExitCanceled(cmd, true) {
			err = ctx.Err()
		}
	default:
	}
	if err != nil {
		out := truncateOutput(stdout.String())
		errText := stderr.String()
		callErr := commandError(label, err, out, errText)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Timeout, Message: callErr.Error()}, nil
		case errors.Is(err, context.Canceled):
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Canceled, Message: callErr.Error()}, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: callErr.Error()}, nil
		}
		return operationresult.Result[*toolshare.Result]{}, callErr
	}
	if stdout.truncated {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: fmt.Sprintf("%s stdout exceeds %d byte limit%s", label, maxRunnerStdout, commandOutputSuffix("", stderr.String()))}, nil
	}
	return resultFromStdoutWithMedia(ctx, stdout.String(), mediaCall, cmd.Dir)
}

func commandError(label string, err error, stdout, stderr string) error {
	return fmt.Errorf("%s failed: %w%s", label, err, commandOutputSuffix(stdout, stderr))
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

func resolveGoExecutable(environment processenv.Environment) (operationresult.Result[string], error) {
	if value, ok := environment.Lookup(goBinaryEnv); ok && strings.TrimSpace(value) != "" {
		return validateGoExecutable(value, goBinaryEnv)
	}
	if goroot, ok := environment.Lookup("GOROOT"); ok && strings.TrimSpace(goroot) != "" {
		candidate := filepath.Join(goroot, "bin", executableName("go"))
		return validateGoExecutable(candidate, "GOROOT")
	}
	goPath, err := environment.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		return operationresult.Result[string]{Status: operationresult.Unavailable, Message: fmt.Sprintf("go executable not found in ElBot service PATH; set %s=/path/to/go in system environment or config .env, or configure service PATH/GOROOT, then restart ElBot", goBinaryEnv)}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: goPath}, nil
}

func validateGoExecutable(path, source string) (operationresult.Result[string], error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return operationresult.Result[string]{Status: operationresult.Unavailable, Message: fmt.Sprintf("%s is empty", source)}, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[string]{Status: operationresult.Unavailable, Message: fmt.Sprintf("%s points to unavailable go executable %q: %v", source, path, err)}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, fmt.Errorf("%s points to unavailable go executable %q: %w", source, path, err)
	}
	if info.IsDir() {
		return operationresult.Result[string]{Status: operationresult.Unavailable, Message: fmt.Sprintf("%s points to a directory, not go executable: %q", source, path)}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: path}, nil
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

type goBuildResult struct {
	Stdout string // 编译成功时收集的标准输出。
	Stderr string // 编译成功时收集的标准错误输出。
}

func runGoBuild(ctx context.Context, root, name string, timeoutMS int, environment processenv.Environment) (operationresult.Result[goBuildResult], error) {
	goPath, err := resolveGoExecutable(environment)
	if err != nil {
		return operationresult.Result[goBuildResult]{}, err
	}
	if goPath.Status != operationresult.Success {
		return operationresult.Result[goBuildResult]{Status: goPath.Status, Message: goPath.Message}, nil
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
	cmd := environment.CommandContext(runCtx, goPath.Data, "build", "-o", binary, ".")
	cmd.Dir = root
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cancelCommand := cmd.Cancel
	cancelRequested := make(chan error, 1)
	cmd.Cancel = func() error {
		err := cancelCommand()
		cancelRequested <- err
		return err
	}
	err = cmd.Run()
	select {
	case cancelErr := <-cancelRequested:
		if cancelErr != nil {
			if !errors.Is(cancelErr, os.ErrProcessDone) && !errors.Is(err, cancelErr) {
				events.AsyncLog(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn,
					EventName: "skill_process_cleanup_failed", Module: "skill", Content: cancelErr.Error(),
					Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(cancelErr)})
			}
		} else if processenv.ExitCanceled(cmd, true) {
			err = runCtx.Err()
		}
	default:
	}
	if err != nil {
		callErr := commandError("go build", err, truncateOutput(stdout.String()), stderr.String())
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return operationresult.Result[goBuildResult]{Status: operationresult.Timeout, Message: callErr.Error()}, nil
		case errors.Is(err, context.Canceled):
			return operationresult.Result[goBuildResult]{Status: operationresult.Canceled, Message: callErr.Error()}, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return operationresult.Result[goBuildResult]{Status: operationresult.Failed, Message: callErr.Error()}, nil
		}
		return operationresult.Result[goBuildResult]{}, callErr
	}
	return operationresult.Result[goBuildResult]{Status: operationresult.Success, Data: goBuildResult{Stdout: stdout.String(), Stderr: stderr.String()}}, nil
}

func detectGoModVersion(ctx context.Context, environment processenv.Environment) (operationresult.Result[string], error) {
	goPath, err := resolveGoExecutable(environment)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if goPath.Status != operationresult.Success {
		return goPath, nil
	}
	cmd := environment.CommandContext(ctx, goPath.Data, "env", "GOVERSION")
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		callErr := fmt.Errorf("detect go version: %w\nstderr:\n%s", err, truncateOutput(stderr.String()))
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return operationresult.Result[string]{Status: operationresult.Failed, Message: callErr.Error()}, nil
		}
		return operationresult.Result[string]{}, callErr
	}
	version, err := parseGoModVersion(stdout.String())
	if err != nil {
		return operationresult.Result[string]{Status: operationresult.Incompatible, Message: err.Error()}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: version}, nil
}
