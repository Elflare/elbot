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

	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
	hookprotocol "elbot/internal/hook/protocol"
)

const (
	hookProtocolVersion       = hookprotocol.Version
	maxHookProtocolFrameBytes = hook.MaxProtocolFrameBytes
	maxHookOutputBase64Bytes  = 10 * 1024 * 1024
	largeOutputRecommendation = "write large media to a file and return outputs[].path or outputs[].url instead of inline base64"
)

// runExec executes the one-shot variant of hook.v2. The process performs a
// request/response handshake, receives one event.handle request, and returns a
// completed result before exiting. Stateful hooks use internal/hook/runtime.
func (m Module) runExec(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, error) {
	argv := make([]string, len(action.Command))
	for i, arg := range action.Command {
		argv[i] = render(arg, event, state)
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return event, actionResult{Error: "command is required"}, fmt.Errorf("command is required")
	}
	runCtx := ctx
	cancel := func() {}
	if action.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
	}
	defer cancel()
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
	params := struct {
		hookprotocol.EventHandleParams
		Runtime map[string]any `json:"runtime"`
	}{
		EventHandleParams: hookprotocol.EventHandleParams{Event: event, Match: hook.EventMatchContext(event)},
		Runtime:           runtime,
	}
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
	request, err := hookprotocol.NewRequest(id, method, params)
	if err != nil {
		return err
	}
	return writeProtocolFrame(w, request)
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
		frame, err := hookprotocol.DecodeFrame([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("parse hook.v2 stdout frame: %w; line=%s", err, shortProtocolLine(line))
		}
		switch frame.Type {
		case "response":
			if err := hookprotocol.ValidateID(frame.ID, "host:"); err != nil {
				return nil, fmt.Errorf("hook.v2 response: %w", err)
			}
			if frame.ID != wantedID {
				return nil, fmt.Errorf("unexpected hook.v2 response id %q while waiting for %q", frame.ID, wantedID)
			}
			if frame.OK == nil || !*frame.OK {
				return nil, fmt.Errorf("hook.v2 request %q failed: %s", wantedID, firstNonEmpty(frame.Error, "plugin returned ok=false"))
			}
			return frame.Result, nil
		case "request":
			if err := hookprotocol.ValidateID(frame.ID, "plugin:"); err != nil {
				return nil, fmt.Errorf("hook.v2 request: %w", err)
			}
			result, requestErr := m.handleProtocolRequest(ctx, event, action, state, frame.Method, frame.Params)
			if err := writeV2Response(stdin, frame.ID, result, requestErr); err != nil {
				return nil, pluginStdinWriteError("response", err)
			}
			if requestErr != nil {
				return nil, requestErr
			}
		case "event":
			if frame.Method == "hook.log" && m.Logger != nil {
				m.Logger.Info("hook.v2 plugin event", "rule", firstNonEmpty(action.source.FinalName, action.ActionName), "params", string(frame.Params))
			}
		default:
			return nil, fmt.Errorf("unsupported hook.v2 frame type %q", frame.Type)
		}
	}
}

func v2EventResult(event hook.Event, action Action, state state, raw json.RawMessage) (actionResult, hook.Event, error) {
	if len(raw) == 0 {
		return actionResult{}, event, nil
	}
	var payload struct {
		hookprotocol.EventResultBase
		Result  string `json:"result"`
		Error   string `json:"error"`
		Matched *bool  `json:"matched"`
		Message *struct {
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

func shortProtocolLine(line string) string {
	line = strings.TrimSpace(line)
	const max = 240
	if len([]rune(line)) <= max {
		return line
	}
	runes := []rune(line)
	return string(runes[:max]) + "..."
}

func (m Module) handleProtocolRequest(ctx context.Context, event hook.Event, action Action, state state, method string, params json.RawMessage) (any, error) {
	if strings.HasPrefix(method, "shared.") {
		if m.Opts.Runtime == nil {
			return nil, fmt.Errorf("hook runtime is not configured")
		}
		return m.Opts.Runtime.SharedState().HandleRequest(method, params)
	}
	switch method {
	case "platform.call":
		paramsMap, err := rawObject(params)
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
		paramsMap, err := rawObject(params)
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
			m.Logger.Info("hook plugin log", "rule", firstNonEmpty(action.source.FinalName, action.ActionName), "params", string(params))
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fmt.Errorf("unsupported hook protocol method %q", method)
	}
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

func writeProtocolFrame(w io.Writer, frame any) error {
	data, err := hookprotocol.EncodeFrame(frame)
	if err != nil {
		return err
	}
	if len(data)+1 > maxHookProtocolFrameBytes {
		return fmt.Errorf("hook protocol stdin frame exceeds %s limit", byteSize(maxHookProtocolFrameBytes))
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
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
