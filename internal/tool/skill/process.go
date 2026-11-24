// 执行 Skill 子进程并收集有界输出。
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

func runMediaCommand(ctx context.Context, label string, cmd *exec.Cmd, mediaCall toolshare.MediaCall) (operationresult.Result[*toolshare.Result], error) {
	stdout := limitedOutput{limit: maxRunnerStdout}
	stderr := limitedOutput{limit: maxRunnerOutput}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := runSkillProcess(ctx, ctx, cmd)
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

func runSkillProcess(runCtx, logCtx context.Context, cmd *exec.Cmd) error {
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
				events.AsyncLog(logCtx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn,
					EventName: "skill_process_cleanup_failed", Module: "skill", Content: cancelErr.Error(),
					Identity: events.LogIdentityFromContext(logCtx), ResultStatus: events.LogStatus(cancelErr)})
			}
		} else if processenv.ExitCanceled(cmd, true) {
			err = runCtx.Err()
		}
	default:
	}
	return err
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
