package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/hook"
)

const (
	execStdoutIgnore  = "ignore"
	execStdoutCapture = "capture"
	execStdoutSend    = "send"
	execStdoutElvena  = "elvena"
	execStdoutOutputs = "outputs"
)

type execPayload struct {
	Event hook.Event        `json:"event"`
	Match hook.MatchContext `json:"match"`
}

type execOutputsPayload struct {
	Outputs []SegmentSpec `json:"outputs"`
	Text    string        `json:"text"`
}

func (m Module) runExec(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, error) {
	command := render(action.Command, event, state)
	if strings.TrimSpace(command) == "" {
		return event, actionResult{Error: "command is required"}, fmt.Errorf("command is required")
	}
	stdoutMode := strings.TrimSpace(action.Stdout)
	if stdoutMode == "" {
		stdoutMode = execStdoutCapture
	}
	if err := validateExecStdout(stdoutMode); err != nil {
		return event, actionResult{Error: err.Error()}, err
	}

	runCtx := ctx
	cancel := func() {}
	if action.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = m.execCwd(action, event, state)
	stdin, err := execStdin(action, event, state)
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return event, actionResult{Result: strings.TrimSpace(stdout.String()), Error: message}, fmt.Errorf("exec failed: %s", message)
	}
	outText := strings.TrimSpace(stdout.String())
	result := actionResult{Result: outText}
	switch stdoutMode {
	case execStdoutIgnore, execStdoutCapture:
		return event, result, nil
	case execStdoutSend:
		if outText != "" {
			out := delivery.Text(outText)
			out.Target = delivery.Target{
				Platform:      render(action.Target.Platform, event, state),
				ScopeID:       render(action.Target.ScopeID, event, state),
				PrivateUserID: render(action.Target.PrivateUserID, event, state),
				GroupID:       render(action.Target.GroupID, event, state),
				Superadmins:   action.Target.Superadmins,
			}
			out = delivery.WithDeliveryTiming(out, render(action.Timing, event, state))
			event.Outputs = append(event.Outputs, out)
		}
		return event, result, nil
	case execStdoutOutputs:
		var payload execOutputsPayload
		if err := json.Unmarshal([]byte(outText), &payload); err != nil {
			return event, actionResult{Result: outText, Error: err.Error()}, fmt.Errorf("parse outputs stdout: %w", err)
		}
		target := delivery.Target{
			Platform:      render(action.Target.Platform, event, state),
			ScopeID:       render(action.Target.ScopeID, event, state),
			PrivateUserID: render(action.Target.PrivateUserID, event, state),
			GroupID:       render(action.Target.GroupID, event, state),
			Superadmins:   action.Target.Superadmins,
		}
		timing := render(action.Timing, event, state)
		for _, seg := range payload.Outputs {
			out, err := buildSegmentOutput(seg, target, timing)
			if err != nil {
				return event, actionResult{Result: outText, Error: err.Error()}, err
			}
			event.Outputs = append(event.Outputs, out)
		}
		if strings.TrimSpace(action.Field) != "" {
			var err error
			event, err = setTextField(event, action.Field, payload.Text)
			if err != nil {
				return event, actionResult{Result: outText, Error: err.Error()}, err
			}
		}
		return event, result, nil
	case execStdoutElvena:
		if m.Opts.Elvena == nil {
			err := fmt.Errorf("elvena dispatcher is not configured")
			return event, actionResult{Result: outText, Error: err.Error()}, err
		}
		var req elvena.Request
		if err := json.Unmarshal([]byte(outText), &req); err != nil {
			return event, actionResult{Result: outText, Error: err.Error()}, fmt.Errorf("parse elvena stdout: %w", err)
		}
		resp, err := m.Opts.Elvena.DispatchElvena(ctx, elvena.Origin{Kind: elvena.OriginHook, Name: firstNonEmpty(action.Name, "rules.exec")}, req)
		if err != nil {
			return event, actionResult{Result: outText, Error: err.Error()}, err
		}
		data, _ := json.Marshal(resp)
		return event, actionResult{Result: string(data)}, nil
	default:
		return event, result, nil
	}
}

func (m Module) execCwd(action Action, event hook.Event, state state) string {
	base := strings.TrimSpace(m.Opts.ConfigDir)
	cwd := strings.TrimSpace(render(action.Cwd, event, state))
	if cwd == "" {
		return base
	}
	if filepath.IsAbs(cwd) || base == "" {
		return cwd
	}
	return filepath.Join(base, cwd)
}

func execStdin(action Action, event hook.Event, state state) (string, error) {
	if strings.TrimSpace(action.Stdin) != "" {
		return render(action.Stdin, event, state), nil
	}
	data, err := json.Marshal(execPayload{Event: event, Match: eventMatchContext(event)})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func validateExecStdout(stdout string) error {
	switch strings.TrimSpace(stdout) {
	case "", execStdoutIgnore, execStdoutCapture, execStdoutSend, execStdoutElvena, execStdoutOutputs:
		return nil
	default:
		return fmt.Errorf("unsupported exec stdout %q", stdout)
	}
}
