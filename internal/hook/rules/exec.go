package rules

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
)

const (
	hookProtocolVersion       = "hook.v2"
	maxHookProtocolFrameBytes = 16 * 1024 * 1024
	maxHookOutputBase64Bytes  = 10 * 1024 * 1024
	largeOutputRecommendation = "write large media to a file and return outputs[].path or outputs[].url instead of inline base64"
)

// runExec executes the one-shot variant of hook.v2. The process performs a
// request/response handshake, receives one event.handle request, and returns a
// completed result before exiting. Stateful hooks use internal/hook/runtime.
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
	if err != nil || len(argv) == 0 {
		if err == nil {
			err = fmt.Errorf("command is required")
		}
		return event, actionResult{Error: err.Error()}, err
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
	stderrTail := newExecStderrTail()
	stderrLogger := newExecStderrLogger(m, action, stderrTail)
	cmd.Stderr = stderrLogger
	if err := cmd.Start(); err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	defer stdin.Close()
	waited := false
	waitExec := func() error {
		if waited {
			return nil
		}
		waited = true
		err := cmd.Wait()
		stderrLogger.Flush()
		return err
	}
	fail := func(result actionResult, source error) (hook.Event, actionResult, error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr := waitExec()
		if contextErr := execContextError(runCtx, action); contextErr != nil {
			source = contextErr
		} else if source == nil {
			source = execProcessError(runCtx, action, waitErr)
		}
		source = withExecStderr(source, stderrTail.String())
		result.Error = source.Error()
		return event, result, source
	}

	reader := bufio.NewReader(stdout)
	runtime := map[string]any{
		"plugin_name": action.source.PluginName,
		"plugin_dir":  action.source.BaseDir,
		"config_path": action.source.ConfigPath,
		"rule_name":   firstNonEmpty(action.source.FinalName, action.ActionName),
		"cwd":         cwd,
	}
	initID := "host:init"
	if err := writeV2Request(stdin, initID, "system.init", map[string]any{"version": hookProtocolVersion, "runtime": runtime}); err != nil {
		return fail(actionResult{}, pluginStdinWriteError("system.init", err))
	}
	if _, err := m.readV2Response(runCtx, reader, stdin, event, action, state, initID); err != nil {
		return fail(actionResult{}, err)
	}
	eventID := "host:event"
	params := map[string]any{"event": event, "match": eventMatchContext(event), "runtime": runtime}
	if err := writeV2Request(stdin, eventID, "event.handle", params); err != nil {
		return fail(actionResult{}, pluginStdinWriteError("event.handle", err))
	}
	rawResult, err := m.readV2Response(runCtx, reader, stdin, event, action, state, eventID)
	if err != nil {
		return fail(actionResult{}, err)
	}
	result, updated, err := v2EventResult(event, action, state, rawResult)
	if err != nil {
		return fail(result, err)
	}
	event = updated
	if err := waitExec(); err != nil {
		err = withExecStderr(execProcessError(runCtx, action, err), stderrTail.String())
		return event, actionResult{Error: err.Error()}, err
	}
	return event, result, nil
}

func writeV2Request(w io.Writer, id, method string, params any) error {
	return writeProtocolFrame(w, map[string]any{"type": "request", "id": id, "method": method, "params": params})
}

func writeV2Response(w io.Writer, id string, result any, sourceErr error) error {
	response := map[string]any{"type": "response", "id": id, "ok": sourceErr == nil}
	if sourceErr != nil {
		response["error"] = sourceErr.Error()
	} else {
		response["result"] = result
	}
	return writeProtocolFrame(w, response)
}

func (m Module) readV2Response(ctx context.Context, reader *bufio.Reader, stdin io.Writer, event hook.Event, action Action, state state, wantedID string) (json.RawMessage, error) {
	for {
		line, err := readProtocolLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("hook.v2 plugin closed stdout while waiting for response %q", wantedID)
			}
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return nil, fmt.Errorf("parse hook.v2 stdout frame: %w; line=%s", err, shortProtocolLine(line))
		}
		switch frameString(frame, "type") {
		case "response":
			id := frameString(frame, "id")
			if !strings.HasPrefix(id, "host:") {
				return nil, fmt.Errorf("hook.v2 response id %q must use host: prefix", id)
			}
			if id != wantedID {
				return nil, fmt.Errorf("unexpected hook.v2 response id %q while waiting for %q", id, wantedID)
			}
			var ok bool
			if raw := frame["ok"]; len(raw) == 0 || json.Unmarshal(raw, &ok) != nil || !ok {
				return nil, fmt.Errorf("hook.v2 request %q failed: %s", wantedID, firstNonEmpty(frameString(frame, "error"), "plugin returned ok=false"))
			}
			return frame["result"], nil
		case "request":
			id := frameString(frame, "id")
			if !strings.HasPrefix(id, "plugin:") {
				return nil, fmt.Errorf("hook.v2 request id %q must use plugin: prefix", id)
			}
			result, requestErr := m.handleProtocolRequest(ctx, event, action, state, frame)
			if err := writeV2Response(stdin, id, result, requestErr); err != nil {
				return nil, pluginStdinWriteError("response", err)
			}
			if requestErr != nil {
				return nil, requestErr
			}
		case "event":
			if frameString(frame, "method") == "hook.log" && m.Logger != nil {
				m.Logger.Info("hook.v2 plugin event", "rule", firstNonEmpty(action.source.FinalName, action.ActionName), "params", string(frame["params"]))
			}
		default:
			return nil, fmt.Errorf("unsupported hook.v2 frame type %q", frameString(frame, "type"))
		}
	}
}

func v2EventResult(event hook.Event, action Action, state state, raw json.RawMessage) (actionResult, hook.Event, error) {
	if len(raw) == 0 {
		return actionResult{}, event, nil
	}
	var payload struct {
		Status      string        `json:"status"`
		Outputs     []SegmentSpec `json:"outputs"`
		Target      Target        `json:"target,omitempty"`
		Timing      string        `json:"timing,omitempty"`
		Result      string        `json:"result"`
		Error       string        `json:"error"`
		Matched     *bool         `json:"matched"`
		PassThrough *bool         `json:"pass_through,omitempty"`
		Message     *struct {
			Text *string `json:"text"`
		} `json:"message"`
		Consume         bool `json:"consume"`
		StopPropagation bool `json:"stop_propagation"`
	}
	if err := hookoutput.DecodeJSON(raw, &payload); err != nil {
		return actionResult{}, event, fmt.Errorf("decode hook.v2 event result: %w", err)
	}
	if status := strings.TrimSpace(payload.Status); status != "" && status != "completed" {
		return actionResult{}, event, fmt.Errorf("one-shot hook.v2 returned unsupported status %q", status)
	}
	result := actionResult{Result: payload.Result, Error: payload.Error, Matched: payload.Matched, PassThrough: payload.PassThrough}
	if len(payload.Outputs) > 0 {
		outputs, err := hookoutput.BuildGroup(hookoutput.Group{Outputs: payload.Outputs, Target: payload.Target, Timing: payload.Timing}, hookoutput.BuildOptions{BaseDir: action.sourceBaseDir(), DefaultTarget: renderedActionTarget(action, event, state), DefaultTiming: render(action.Timing, event, state)})
		if err != nil {
			return result, event, err
		}
		event.Outputs = append(event.Outputs, outputs...)
	}
	if payload.Message != nil && payload.Message.Text != nil {
		field := strings.TrimSpace(action.Field)
		if field == "" {
			field = "message.text"
		}
		updated, err := setTextField(event, field, *payload.Message.Text)
		if err != nil {
			return result, event, err
		}
		event = updated
	}
	if payload.Consume {
		event.Control.Consume = true
	}
	if payload.StopPropagation {
		event.Control.StopPropagation = true
	}
	return result, event, nil
}

func (m Module) runExecV1(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, error) {
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
	stderrTail := newExecStderrTail()
	stderrLogger := newExecStderrLogger(m, action, stderrTail)
	cmd.Stderr = stderrLogger
	if err := cmd.Start(); err != nil {
		return event, actionResult{Error: err.Error()}, err
	}
	defer stdin.Close()
	waited := false
	waitExec := func() error {
		if waited {
			return nil
		}
		waited = true
		err := cmd.Wait()
		stderrLogger.Flush()
		return err
	}
	failExec := func(result actionResult, err error, kill bool) (hook.Event, actionResult, error) {
		if kill && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr := waitExec()
		if ctxErr := execContextError(runCtx, action); ctxErr != nil {
			err = ctxErr
		} else if err == nil {
			err = execProcessError(runCtx, action, waitErr)
		}
		err = withExecStderr(err, stderrTail.String())
		result.Error = err.Error()
		return event, result, err
	}

	init := map[string]any{
		"type":    "init",
		"version": hookProtocolVersion,
		"event":   event,
		"match":   eventMatchContext(event),
		"runtime": map[string]any{
			"plugin_name": action.source.PluginName,
			"plugin_dir":  action.source.BaseDir,
			"config_path": action.source.ConfigPath,
			"rule_name":   firstNonEmpty(action.source.FinalName, action.ActionName),
			"cwd":         cwd,
		},
	}
	if err := writeProtocolFrame(stdin, init); err != nil {
		return failExec(actionResult{}, pluginStdinWriteError("init", err), true)
	}

	done := false
	result := actionResult{}
	stdoutReader := bufio.NewReader(stdout)
	lineNo := 0
	for {
		line, err := readProtocolLine(stdoutReader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return failExec(actionResult{}, err, true)
		}
		lineNo++
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			err = fmt.Errorf("parse hook protocol frame from plugin stdout line %d: %w; line=%s", lineNo, err, shortProtocolLine(line))
			return failExec(actionResult{}, err, true)
		}
		updated, frameResult, frameDone, err := m.handleProtocolFrame(runCtx, stdin, event, action, state, frame, lineNo, line)
		if err != nil {
			return failExec(frameResult, err, true)
		}
		event = updated
		if frameDone {
			result = frameResult
			done = true
		}
	}
	if err := waitExec(); err != nil {
		err = withExecStderr(execProcessError(runCtx, action, err), stderrTail.String())
		return event, actionResult{Error: err.Error()}, err
	}
	if !done {
		err := fmt.Errorf("hook protocol missing done frame from plugin")
		err = withExecStderr(err, stderrTail.String())
		return event, actionResult{Error: err.Error()}, err
	}
	return event, result, nil
}

func readProtocolLine(reader *bufio.Reader) (string, error) {
	var data []byte
	for {
		part, err := reader.ReadSlice('\n')
		data = append(data, part...)
		if len(data) > maxHookProtocolFrameBytes {
			return "", fmt.Errorf("hook protocol stdout frame exceeds %s limit; %s", byteSize(maxHookProtocolFrameBytes), largeOutputRecommendation)
		}
		switch {
		case err == nil:
			return string(data), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(data) == 0 {
				return "", io.EOF
			}
			return string(data), nil
		default:
			return "", fmt.Errorf("read hook plugin stdout: %w", err)
		}
	}
}

func (m Module) handleProtocolFrame(ctx context.Context, stdin io.Writer, event hook.Event, action Action, state state, frame map[string]json.RawMessage, lineNo int, line string) (hook.Event, actionResult, bool, error) {
	typ := frameString(frame, "type")
	switch typ {
	case "output":
		outputs, err := protocolFrameOutputs(frame, action, renderedActionTarget(action, event, state), render(action.Timing, event, state), lineNo, typ, line)
		if err != nil {
			return event, actionResult{Error: err.Error()}, false, err
		}
		event.Outputs = append(event.Outputs, outputs...)
		if id := frameString(frame, "id"); id != "" {
			if err := writeProtocolResponse(stdin, id, map[string]any{"queued": true}, nil); err != nil {
				err = pluginStdinWriteError("response", err)
				return event, actionResult{Error: err.Error()}, false, err
			}
		}
		return event, actionResult{}, false, nil
	case "request":
		result, err := m.handleProtocolRequest(ctx, event, action, state, frame)
		if id := frameString(frame, "id"); id != "" {
			if writeErr := writeProtocolResponse(stdin, id, result, err); writeErr != nil {
				writeErr = pluginStdinWriteError("response", writeErr)
				return event, actionResult{Error: writeErr.Error()}, false, writeErr
			}
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
		return event, actionResult{Error: msg}, false, fmt.Errorf("hook protocol error frame from plugin: %s", msg)
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
		m.audit("hook.platform_call", "platform", platformName, "api", api, "rule", firstNonEmpty(action.source.FinalName, action.ActionName))
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
		outputs, err := protocolOutputsFromMap(paramsMap, action, renderedActionTarget(action, event, state), render(action.Timing, event, state), "params.outputs")
		if err != nil {
			return nil, err
		}
		receipt, err := m.sendProtocolOutput(ctx, event, outputs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sent": len(outputs), "receipts": []delivery.Receipt{receipt}}, nil
	case "message.get_reply":
		return map[string]any{"message_id": event.Platform.ReplyToMessageID, "available": event.Platform.ReplyToMessageID != ""}, nil
	case "message.get":
		return map[string]any{"available": false}, nil
	case "hook.log":
		if m.Logger != nil {
			m.Logger.Info("hook plugin log", "rule", firstNonEmpty(action.source.FinalName, action.ActionName), "params", string(frame["params"]))
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fmt.Errorf("unsupported hook protocol method %q", method)
	}
}

func protocolFrameOutputs(frame map[string]json.RawMessage, action Action, target Target, timing string, lineNo int, typ, line string) ([]delivery.Output, error) {
	outputs, err := protocolOutputsFromMap(frame, action, target, timing, "outputs")
	if err == nil {
		return outputs, nil
	}
	if len(frame["outputs"]) == 0 {
		return nil, protocolFrameError(lineNo, typ, line, "missing required field \"outputs\"; output frames must be {\"type\":\"output\",\"outputs\":[{...}]}. The \"outputs\" value is an array of segment objects")
	}
	return nil, protocolFrameError(lineNo, typ, line, err.Error())
}

func protocolOutputsFromMap(values map[string]json.RawMessage, action Action, defaultTarget Target, defaultTiming, fieldName string) ([]delivery.Output, error) {
	raw := values["outputs"]
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing required field %q; expected an array of segment objects", fieldName)
	}
	var specs []SegmentSpec
	if err := hookoutput.DecodeJSON(raw, &specs); err != nil {
		return nil, fmt.Errorf("invalid field %q: %w", fieldName, err)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("field %q must contain at least one segment object", fieldName)
	}
	var target Target
	if rawTarget := values["target"]; len(rawTarget) > 0 {
		if err := hookoutput.DecodeJSON(rawTarget, &target); err != nil {
			return nil, fmt.Errorf("invalid field \"target\": %w", err)
		}
	}
	return hookoutput.BuildGroup(hookoutput.Group{Outputs: specs, Target: target, Timing: rawString(values["timing"])}, hookoutput.BuildOptions{BaseDir: action.sourceBaseDir(), DefaultTarget: defaultTarget, DefaultTiming: defaultTiming})
}

func renderedActionTarget(action Action, event hook.Event, state state) hookoutput.Target {
	return hookoutput.Target{
		Platform: render(action.Target.Platform, event, state), ScopeID: render(action.Target.ScopeID, event, state),
		PrivateUserID: render(action.Target.PrivateUserID, event, state), GroupID: render(action.Target.GroupID, event, state), Superadmins: action.Target.Superadmins,
	}
}

func (m Module) sendProtocolOutput(ctx context.Context, event hook.Event, outputs []delivery.Output) (delivery.Receipt, error) {
	if m.Opts.Send != nil {
		return m.Opts.Send(ctx, delivery.Target{}, outputs)
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

func pluginStdinWriteError(frame string, err error) error {
	frame = strings.TrimSpace(frame)
	if frame == "" {
		frame = "protocol"
	}
	return fmt.Errorf("write hook plugin stdin %s frame failed; plugin may have exited early or closed stdin: %w", frame, err)
}

func execProcessError(ctx context.Context, action Action, err error) error {
	if ctxErr := execContextError(ctx, action); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return fmt.Errorf("hook plugin exec failed: %w", err)
	}
	return fmt.Errorf("hook plugin exec failed")
}

func execContextError(ctx context.Context, action Action) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("hook plugin exec canceled: %w", context.Canceled)
	}
	if action.TimeoutSeconds > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("hook plugin exec timed out after %ds: %w", action.TimeoutSeconds, context.DeadlineExceeded)
	}
	return nil
}

func withExecStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if err == nil || stderr == "" {
		return err
	}
	return fmt.Errorf("%w\nstderr:\n%s", err, stderr)
}

type execStderrTail struct {
	mu      sync.Mutex
	lines   []string
	dropped int
}

func newExecStderrTail() *execStderrTail {
	return &execStderrTail{}
}

func (t *execStderrTail) Add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	const maxLines = 20
	if len(t.lines) >= maxLines {
		copy(t.lines, t.lines[1:])
		t.lines[len(t.lines)-1] = line
		t.dropped++
		return
	}
	t.lines = append(t.lines, line)
}

func (t *execStderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return ""
	}
	out := strings.Join(t.lines, "\n")
	const maxRunes = 2000
	runes := []rune(out)
	if len(runes) > maxRunes {
		out = string(runes[len(runes)-maxRunes:])
	}
	if t.dropped > 0 {
		return fmt.Sprintf("...(%d earlier stderr lines omitted)\n%s", t.dropped, out)
	}
	return out
}

type execStderrLogger struct {
	mu      sync.Mutex
	module  Module
	action  Action
	tail    *execStderrTail
	pending strings.Builder
}

func newExecStderrLogger(module Module, action Action, tail *execStderrTail) *execStderrLogger {
	return &execStderrLogger{module: module, action: action, tail: tail}
}

func (w *execStderrLogger) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		if i := strings.IndexByte(string(p), '\n'); i >= 0 {
			w.pending.Write(p[:i])
			w.emitLocked(w.pending.String())
			w.pending.Reset()
			p = p[i+1:]
			continue
		}
		w.pending.Write(p)
		const maxPending = 4096
		if w.pending.Len() >= maxPending {
			w.emitLocked(w.pending.String())
			w.pending.Reset()
		}
		break
	}
	return n, nil
}

func (w *execStderrLogger) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending.Len() == 0 {
		return
	}
	w.emitLocked(w.pending.String())
	w.pending.Reset()
}

func (w *execStderrLogger) emitLocked(line string) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if w.tail != nil {
		w.tail.Add(line)
	}
	if line != "" && w.module.Logger != nil {
		w.module.Logger.Info("hook exec stderr", "rule", firstNonEmpty(w.action.source.FinalName, w.action.ActionName), "line", line)
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
