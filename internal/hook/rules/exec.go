package rules

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"elbot/internal/delivery"
	"elbot/internal/hook"
)

const hookProtocolVersion = "hook.v1"

func (m Module) runExec(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, error) {
	command := render(action.Command, event, state)
	if strings.TrimSpace(command) == "" {
		return event, actionResult{Error: "command is required"}, fmt.Errorf("command is required")
	}
	runCtx := ctx
	cancel := func() {}
	if action.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	argv, err := splitExecCommand(command)
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	if len(argv) == 0 {
		return event, actionResult{Error: "command is required"}, fmt.Errorf("command is required")
	}
	cwd, err := m.execCwd(action, event, state)
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	if err := cmd.Start(); err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	defer stdin.Close()
	go m.logExecStderr(stderr, action)

	init := map[string]any{
		"type":    "init",
		"version": hookProtocolVersion,
		"event":   event,
		"match":   eventMatchContext(event),
		"runtime": map[string]any{
			"plugin_name": action.source.PluginName,
			"plugin_dir":  action.source.BaseDir,
			"config_path": action.source.ConfigPath,
			"rule_name":   firstNonEmpty(action.source.FinalName, action.Name),
			"cwd":         cwd,
		},
	}
	if err := writeProtocolFrame(stdin, init); err != nil {
		_ = cmd.Process.Kill()
		return event, actionResult{Error: err.Error()}, err
	}

	done := false
	result := actionResult{}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			_ = cmd.Process.Kill()
			return event, actionResult{Error: err.Error()}, fmt.Errorf("parse hook protocol frame: %w", err)
		}
		updated, frameResult, frameDone, err := m.handleProtocolFrame(runCtx, stdin, event, action, state, frame)
		if err != nil {
			_ = cmd.Process.Kill()
			return event, frameResult, err
		}
		event = updated
		if frameDone {
			result = frameResult
			done = true
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		return event, actionResult{Error: err.Error()}, err
	}
	if err := cmd.Wait(); err != nil {
		return event, actionResult{Error: err.Error()}, fmt.Errorf("exec failed: %w", err)
	}
	if !done {
		err := fmt.Errorf("hook protocol missing done frame")
		return event, actionResult{Error: err.Error()}, err
	}
	return event, result, nil
}

func (m Module) handleProtocolFrame(ctx context.Context, stdin io.Writer, event hook.Event, action Action, state state, frame map[string]json.RawMessage) (hook.Event, actionResult, bool, error) {
	typ := frameString(frame, "type")
	switch typ {
	case "output":
		var seg SegmentSpec
		if err := json.Unmarshal(frame["output"], &seg); err != nil {
			return event, actionResult{Error: err.Error()}, false, err
		}
		out, err := buildSegmentOutput(resolveSegmentSpecPath(seg, action.sourceBaseDir()), delivery.Target{}, render(action.Timing, event, state))
		if err != nil {
			return event, actionResult{Error: err.Error()}, false, err
		}
		event.Outputs = append(event.Outputs, out)
		if id := frameString(frame, "id"); id != "" {
			_ = writeProtocolResponse(stdin, id, map[string]any{"queued": true}, nil)
		}
		return event, actionResult{}, false, nil
	case "request":
		result, err := m.handleProtocolRequest(ctx, event, action, state, frame)
		if id := frameString(frame, "id"); id != "" {
			_ = writeProtocolResponse(stdin, id, result, err)
		}
		if err != nil {
			return event, actionResult{Error: err.Error()}, false, err
		}
		return event, actionResult{}, false, nil
	case "done":
		matched := frameBoolDefault(frame, "matched", true)
		res := actionResult{
			Result:  frameString(frame, "result"),
			Error:   frameString(frame, "error"),
			Matched: &matched,
		}
		if !matched {
			return event, res, true, nil
		}
		if raw := frame["message"]; len(raw) > 0 {
			var msg map[string]json.RawMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				return event, actionResult{Error: err.Error(), Matched: &matched}, true, err
			}
			if textRaw := msg["text"]; len(textRaw) > 0 {
				var text string
				if err := json.Unmarshal(textRaw, &text); err != nil {
					return event, actionResult{Error: err.Error(), Matched: &matched}, true, err
				}
				field := strings.TrimSpace(action.Field)
				if field == "" {
					field = "message.text"
				}
				var err error
				event, err = setTextField(event, field, text)
				if err != nil {
					return event, actionResult{Error: err.Error(), Matched: &matched}, true, err
				}
			}
		}
		if frameBoolDefault(frame, "consume", false) {
			event.Control.Consume = true
		}
		if frameBoolDefault(frame, "stop_propagation", false) {
			event.Control.StopPropagation = true
		}
		return event, res, true, nil
	case "error":
		msg := firstNonEmpty(frameString(frame, "error"), frameString(frame, "message"))
		if msg == "" {
			msg = "hook protocol error frame"
		}
		return event, actionResult{Error: msg}, false, fmt.Errorf("hook protocol error: %s", msg)
	default:
		return event, actionResult{Error: "unsupported protocol frame"}, false, fmt.Errorf("unsupported hook protocol frame %q", typ)
	}
}

func (m Module) handleProtocolRequest(ctx context.Context, event hook.Event, action Action, state state, frame map[string]json.RawMessage) (any, error) {
	method := frameString(frame, "method")
	switch method {
	case "platform.call":
		paramsMap, err := rawObject(frame["params"])
		if err != nil {
			return nil, err
		}
		platformName := strings.TrimSpace(rawString(paramsMap["platform"]))
		if platformName == "" {
			platformName = event.Platform.Name
		}
		if event.Platform.Name != "" && platformName != event.Platform.Name {
			return nil, fmt.Errorf("platform.call can only call current platform %q", event.Platform.Name)
		}
		api := rawString(paramsMap["api"])
		callParams := map[string]any{}
		if raw := paramsMap["params"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &callParams); err != nil {
				return nil, err
			}
		}
		if m.Opts.PlatformCallers == nil {
			return nil, fmt.Errorf("platform callers are not configured")
		}
		caller, ok := m.Opts.PlatformCallers.PlatformCaller(platformName)
		if !ok || caller == nil {
			return nil, fmt.Errorf("platform %q does not support api calls", platformName)
		}
		m.audit("hook.platform_call", "platform", platformName, "api", api, "rule", firstNonEmpty(action.source.FinalName, action.Name))
		resp, err := caller.CallPlatformAPI(ctx, api, callParams)
		if err != nil {
			return nil, err
		}
		var decoded any
		if len(resp) > 0 && json.Unmarshal(resp, &decoded) == nil {
			return decoded, nil
		}
		return string(resp), nil
	case "output.send":
		paramsMap, err := rawObject(frame["params"])
		if err != nil {
			return nil, err
		}
		var seg SegmentSpec
		if err := json.Unmarshal(paramsMap["output"], &seg); err != nil {
			return nil, err
		}
		out, err := buildSegmentOutput(resolveSegmentSpecPath(seg, action.sourceBaseDir()), delivery.Target{}, render(action.Timing, event, state))
		if err != nil {
			return nil, err
		}
		return m.sendProtocolOutput(ctx, event, out)
	case "message.get_reply":
		return map[string]any{"message_id": event.Platform.ReplyToMessageID, "available": event.Platform.ReplyToMessageID != ""}, nil
	case "message.get":
		return map[string]any{"available": false}, nil
	case "hook.log":
		if m.Logger != nil {
			m.Logger.Info("hook plugin log", "rule", firstNonEmpty(action.source.FinalName, action.Name), "params", string(frame["params"]))
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fmt.Errorf("unsupported hook protocol method %q", method)
	}
}

func (m Module) sendProtocolOutput(ctx context.Context, event hook.Event, out delivery.Output) (delivery.Receipt, error) {
	if m.Opts.Send != nil {
		return m.Opts.Send(ctx, out.Target, out)
	}
	return delivery.Receipt{}, fmt.Errorf("output sender is not configured")
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return strings.TrimSpace(value)
}

func frameString(frame map[string]json.RawMessage, key string) string {
	return rawString(frame[key])
}

func frameBoolDefault(frame map[string]json.RawMessage, key string, fallback bool) bool {
	raw := frame[key]
	if len(raw) == 0 {
		return fallback
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

func writeProtocolFrame(w io.Writer, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func writeProtocolResponse(w io.Writer, id string, result any, sourceErr error) error {
	resp := map[string]any{"type": "response", "id": id, "ok": sourceErr == nil}
	if sourceErr != nil {
		resp["error"] = sourceErr.Error()
	} else {
		resp["result"] = result
	}
	return writeProtocolFrame(w, resp)
}

func (m Module) logExecStderr(r io.Reader, action Action) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && m.Logger != nil {
			m.Logger.Info("hook exec stderr", "rule", firstNonEmpty(action.source.FinalName, action.Name), "line", line)
		}
	}
	if err := scanner.Err(); err != nil && m.Logger != nil {
		m.Logger.Warn("hook exec stderr read failed", "rule", firstNonEmpty(action.source.FinalName, action.Name), "error", err)
	}
}

func (m Module) execCwd(action Action, event hook.Event, state state) (string, error) {
	base := strings.TrimSpace(action.sourceBaseDir())
	if base == "" {
		base = strings.TrimSpace(m.Opts.ConfigDir)
	}
	cwd := strings.TrimSpace(render(action.Cwd, event, state))
	if cwd == "" {
		return base, nil
	}
	if action.hasStrictDir() {
		if filepath.IsAbs(cwd) {
			return "", fmt.Errorf("cwd %q must be relative inside plugin directory", cwd)
		}
		joined := filepath.Join(base, filepath.Clean(cwd))
		if !pathWithin(action.sourceStrictDir(), joined) {
			return "", fmt.Errorf("cwd %q escapes plugin directory", cwd)
		}
		return joined, nil
	}
	if filepath.IsAbs(cwd) || base == "" {
		return cwd, nil
	}
	return filepath.Join(base, cwd), nil
}

func (a Action) sourceBaseDir() string {
	return strings.TrimSpace(a.source.BaseDir)
}

func (a Action) sourceStrictDir() string {
	return strings.TrimSpace(a.source.StrictDir)
}

func (a Action) hasStrictDir() bool {
	return strings.TrimSpace(a.source.StrictDir) != ""
}

func resolveSegmentSpecPath(spec SegmentSpec, base string) SegmentSpec {
	spec.Path = resolveLocalPath(spec.Path, base)
	return spec
}

func resolveLocalPath(path, base string) string {
	path = strings.TrimSpace(path)
	if path == "" || base == "" || filepath.IsAbs(path) || delivery.IsDirectMediaSource(path) {
		return path
	}
	return filepath.Join(base, path)
}

func splitExecCommand(command string) ([]string, error) {
	var args []string
	var b strings.Builder
	runes := []rune(command)
	var quote rune
	tokenStarted := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == 92 && i+1 < len(runes) && (runes[i+1] == quote || runes[i+1] == 92) {
				b.WriteRune(runes[i+1])
				i++
				continue
			}
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			if tokenStarted {
				args = append(args, b.String())
				b.Reset()
				tokenStarted = false
			}
			continue
		}
		if r == 39 || r == 34 {
			quote = r
			tokenStarted = true
			continue
		}
		b.WriteRune(r)
		tokenStarted = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if tokenStarted {
		args = append(args, b.String())
	}
	return args, nil
}
