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
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			_ = cmd.Process.Kill()
			err = fmt.Errorf("parse hook protocol frame at stdout line %d: %w; line=%s", lineNo, err, shortProtocolLine(line))
			return event, actionResult{Error: err.Error()}, err
		}
		updated, frameResult, frameDone, err := m.handleProtocolFrame(runCtx, stdin, event, action, state, frame, lineNo, line)
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

func (m Module) handleProtocolFrame(ctx context.Context, stdin io.Writer, event hook.Event, action Action, state state, frame map[string]json.RawMessage, lineNo int, line string) (hook.Event, actionResult, bool, error) {
	typ := frameString(frame, "type")
	switch typ {
	case "output":
		outputs, err := protocolFrameOutputs(frame, action, render(action.Timing, event, state), lineNo, typ, line)
		if err != nil {
			return event, actionResult{Error: err.Error()}, false, err
		}
		event.Outputs = append(event.Outputs, outputs...)
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
		err := protocolFrameError(lineNo, typ, line, fmt.Sprintf("unsupported hook protocol frame %q", typ))
		return event, actionResult{Error: "unsupported protocol frame"}, false, err
	}
}

func protocolFrameError(lineNo int, typ, line, message string) error {
	if typ == "" {
		typ = "<missing>"
	}
	return fmt.Errorf("hook protocol stdout line %d frame type %q: %s; line=%s", lineNo, typ, message, shortProtocolLine(line))
}

func shortProtocolLine(line string) string {
	line = strings.TrimSpace(line)
	const max = 240
	if len([]rune(line)) <= max {
		return line
	}
	runes := []rune(line)
	return string(runes[:max]) + "..."
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
		outputs, err := protocolOutputsFromRaw(paramsMap["outputs"], action, render(action.Timing, event, state), "params.outputs")
		if err != nil {
			return nil, err
		}
		receipts := make([]delivery.Receipt, 0, len(outputs))
		for _, out := range outputs {
			receipt, err := m.sendProtocolOutput(ctx, event, out)
			if err != nil {
				return nil, err
			}
			receipts = append(receipts, receipt)
		}
		return map[string]any{"sent": len(receipts), "receipts": receipts}, nil
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

func protocolFrameOutputs(frame map[string]json.RawMessage, action Action, timing string, lineNo int, typ, line string) ([]delivery.Output, error) {
	outputs, err := protocolOutputsFromRaw(frame["outputs"], action, timing, "outputs")
	if err == nil {
		return outputs, nil
	}
	if len(frame["outputs"]) == 0 {
		return nil, protocolFrameError(lineNo, typ, line, "missing required field \"outputs\"; output frames must be {\"type\":\"output\",\"outputs\":[{...}]}. The \"outputs\" value is an array of segment objects")
	}
	return nil, protocolFrameError(lineNo, typ, line, err.Error())
}

func protocolOutputsFromRaw(raw json.RawMessage, action Action, timing, fieldName string) ([]delivery.Output, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing required field %q; expected an array of segment objects", fieldName)
	}
	var specs []SegmentSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		return nil, fmt.Errorf("invalid field %q: %w", fieldName, err)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("field %q must contain at least one segment object", fieldName)
	}
	outputs := make([]delivery.Output, 0, len(specs))
	for _, seg := range specs {
		out, err := buildSegmentOutput(resolveSegmentSpecPath(seg, action.sourceBaseDir()), delivery.Target{}, timing)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
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
