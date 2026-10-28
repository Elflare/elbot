// 执行一次性插件进程并处理 hook.v2 交互与宿主请求。

package rules

import (
	"bufio"
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
	hookoutput "elbot/internal/hook/share/output"
	hookprotocol "elbot/internal/hook/share/protocol"
	"elbot/internal/llm"
	"elbot/internal/processenv"
)

const (
	hookProtocolVersion       = hookprotocol.Version
	maxHookProtocolFrameBytes = share.MaxProtocolFrameBytes
	maxHookOutputBase64Bytes  = 10 * 1024 * 1024
	largeOutputRecommendation = "write large media to a file and return outputs[].path or outputs[].url instead of inline base64"
)

// runExec executes the one-shot variant of hook.v2. The process performs a
// request/response handshake, receives one event.handle request, and returns a
// completed result before exiting. Stateful hooks use internal/hook/runtime.
func (m Module) runExec(ctx context.Context, event share.Event, action Action, state state) (share.Event, actionResult, globalevents.LogResult, error) {
	argv := make([]string, len(action.Command))
	for i, arg := range action.Command {
		argv[i] = render(arg, event, state)
	}
	if strings.TrimSpace(argv[0]) == "" {
		return event, actionResult{Error: "command is required"}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "command is required; process not started"}, fmt.Errorf("command is required")
	}
	runCtx := ctx
	cancel := func() {}
	if action.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(action.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	cwd, err := m.execCwd(action, event, state)
	if err != nil {
		return event, actionResult{Error: err.Error()}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	processEnv := action.source.ProcessEnv
	cmd := processEnv.CommandContext(runCtx, argv[0], argv[1:]...)
	killProcessTree := sync.OnceValue(func() error { return processenv.KillProcessTree(cmd) })
	// Command.Wait joins its cancellation callback. Keep its termination fact
	// separate from a canceled context and from cleanup after a protocol fault.
	cancelRequested := make(chan error, 1)
	cmd.Cancel = func() error {
		err := killProcessTree()
		cancelRequested <- err
		return err
	}
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return event, actionResult{Error: err.Error()}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return event, actionResult{Error: err.Error()}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	stderrTail := newExecStderrTail()
	stderrLogger := newExecStderrLogger(ctx, m, action, stderrTail)
	cmd.Stderr = stderrLogger
	if err := cmd.Start(); err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return event, actionResult{Error: err.Error()}, logResult, err
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
	fail := func(result actionResult, logResult globalevents.LogResult, source error) (share.Event, actionResult, globalevents.LogResult, error) {
		source = errors.Join(source, killProcessTree())
		_ = waitExec()
		if contextErr := execContextError(runCtx, action); contextErr != nil {
			logResult.Content += "\ncontext_error=" + contextErr.Error()
		}
		source = withExecStderr(source, stderrTail.String())
		result.Error = source.Error()
		if stderrTail.String() != "" {
			logResult.Content += "\nstderr=" + stderrTail.String()
		}
		return event, result, logResult, source
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
		return fail(actionResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "write system.init: " + err.Error()}, pluginStdinWriteError("system.init", err))
	}
	if _, logResult, err := m.readV2Response(runCtx, reader, stdin, event, action, state, initID); err != nil {
		return fail(actionResult{}, logResult, err)
	}
	eventID := "host:event"
	params := struct {
		hookprotocol.EventHandleParams
		Runtime map[string]any `json:"runtime"`
	}{
		EventHandleParams: hookprotocol.EventHandleParams{Event: event, Match: share.EventMatchContext(event)},
		Runtime:           runtime,
	}
	if err := writeV2Request(stdin, eventID, "event.handle", params); err != nil {
		return fail(actionResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "write event.handle: " + err.Error()}, pluginStdinWriteError("event.handle", err))
	}
	rawResult, logResult, err := m.readV2Response(runCtx, reader, stdin, event, action, state, eventID)
	if err != nil {
		return fail(actionResult{}, logResult, err)
	}
	result, updated, err := m.v2EventResult(runCtx, event, action, state, rawResult)
	if err != nil {
		return fail(result, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err)
	}
	event = updated
	if err := waitExec(); err != nil {
		logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "process exit: " + err.Error()}
		if runCtx.Err() != nil {
			logResult.Content += "\ncontext_error=" + runCtx.Err().Error()
		}
		if runCtx.Err() != nil {
			select {
			case cancelErr := <-cancelRequested:
				if cancelErr != nil {
					err = errors.Join(err, cancelErr)
					logResult.Content += "\nterminate_error=" + cancelErr.Error()
				} else if processenv.ExitCanceled(cmd, true) {
					err = execContextError(runCtx, action)
				}
			default:
				if err == runCtx.Err() {
					err = execContextError(runCtx, action)
				}
			}
		}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		err = execProcessError(err)
		err = withExecStderr(err, stderrTail.String())
		return event, actionResult{Error: err.Error()}, logResult, err
	}
	return event, result, logResult, nil
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

func (m Module) readV2Response(ctx context.Context, reader *bufio.Reader, stdin io.Writer, event share.Event, action Action, state state, wantedID string) (json.RawMessage, globalevents.LogResult, error) {
	for {
		line, err := readProtocolLine(reader)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, io.EOF) {
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ctx.Err().Error()}
				if ctx.Err() == context.Canceled {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
				return nil, logResult, execContextError(ctx, action)
			}
			if errors.Is(err, io.EOF) {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook.v2 plugin closed stdout while waiting for response %q", wantedID)}, fmt.Errorf("hook.v2 plugin closed stdout while waiting for response %q", wantedID)
			}
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		frame, err := hookprotocol.DecodeFrame([]byte(line))
		if err != nil {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("parse hook.v2 stdout frame: %v; line=%s", err, shortProtocolLine(line))}, fmt.Errorf("parse hook.v2 stdout frame: %w; line=%s", err, shortProtocolLine(line))
		}
		switch frame.Type {
		case "response":
			if err := hookprotocol.ValidateID(frame.ID, "host:"); err != nil {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook.v2 response: %v", err)}, fmt.Errorf("hook.v2 response: %w", err)
			}
			if frame.ID != wantedID {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("unexpected hook.v2 response id %q while waiting for %q", frame.ID, wantedID)}, fmt.Errorf("unexpected hook.v2 response id %q while waiting for %q", frame.ID, wantedID)
			}
			if frame.OK == nil || !*frame.OK {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook.v2 request %q failed: %s", wantedID, firstNonEmpty(frame.Error, "plugin returned ok=false"))}, fmt.Errorf("hook.v2 request %q failed: %s", wantedID, firstNonEmpty(frame.Error, "plugin returned ok=false"))
			}
			return frame.Result, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: wantedID + " completed"}, nil
		case "request":
			if err := hookprotocol.ValidateID(frame.ID, "plugin:"); err != nil {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook.v2 request: %v", err)}, fmt.Errorf("hook.v2 request: %w", err)
			}
			result, logResult, requestErr := m.handleProtocolRequest(ctx, event, action, state, frame.Method, frame.Params)
			if err := writeV2Response(stdin, frame.ID, result, requestErr); err != nil {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "response_write_error=" + err.Error() + "\nplugin_request=" + logResult.Content}, pluginStdinWriteError("response", err)
			}
			if requestErr != nil {
				logResult.Content = "plugin request " + frame.Method + " aborted: " + logResult.Content
				return nil, logResult, requestErr
			}
		case "event":
			if frame.Method == "hook.log" {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelInfo,
						EventName:    "hook_v2_plugin_event",
						Module:       "hook",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("%s\nrule=%v", "hook.v2 plugin event"+": "+string(frame.Params), firstNonEmpty(action.source.FinalName, action.ActionName)),
						ContentLimit: 4096,
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					m.Opts.Log(ctx, record)
				}
			}
		default:
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("unsupported hook.v2 frame type %q", frame.Type)}, fmt.Errorf("unsupported hook.v2 frame type %q", frame.Type)
		}
	}
}

func (m Module) v2EventResult(ctx context.Context, event share.Event, action Action, state state, raw json.RawMessage) (actionResult, share.Event, error) {
	if len(raw) == 0 {
		return actionResult{}, event, nil
	}
	var payload struct {
		hookprotocol.EventResultBase
		Result          string `json:"result"`
		Error           string `json:"error"`
		Matched         *bool  `json:"matched"`
		Consume         bool   `json:"consume"`
		StopPropagation bool   `json:"stop_propagation"`
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
	if payload.Message != nil && payload.Message.Segments != nil {
		segments, err := m.Opts.BuildMessageSegments(ctx, *payload.Message.Segments, action.sourceBaseDir())
		if err != nil {
			return result, event, err
		}
		event.Message.Segments = segments
		if event.Point == share.PointToolCallCompleted {
			event.Tool.Result = llm.SegmentsTextOnly(segments)
		}
	} else if payload.Message != nil && payload.Message.Text != nil {
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

func (m Module) handleProtocolRequest(ctx context.Context, event share.Event, action Action, state state, method string, params json.RawMessage) (any, globalevents.LogResult, error) {
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "protocol request " + method + " handled"}
	if strings.HasPrefix(method, "media.") {
		result, err := m.Opts.Runtime.MediaRequest(ctx, action.sourceBaseDir(), method, params)
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return result, logResult, err
	}
	if strings.HasPrefix(method, "shared.") {
		result, err := m.Opts.Runtime.SharedRequest(method, params)
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		}
		return result, logResult, err
	}
	switch method {
	case "platform.call":
		paramsMap, err := rawObject(params)
		if err != nil {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		platformName, err := rawString(paramsMap["platform"])
		if err != nil {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		if platformName == "" {
			platformName = event.Platform.Name
		}
		if event.Platform.Name != "" && platformName != event.Platform.Name {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("platform.call can only call current platform %q", event.Platform.Name)}, fmt.Errorf("platform.call can only call current platform %q", event.Platform.Name)
		}
		api, err := rawString(paramsMap["api"])
		if err != nil {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		callParams := map[string]any{}
		if raw := paramsMap["params"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &callParams); err != nil {
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
			}
		}
		resp, err := m.Opts.CallPlatformAPI(ctx, platformName, api, callParams)
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		{
			content := map[string]any{"platform": platformName, "api": api, "rule": firstNonEmpty(action.source.FinalName, action.ActionName), "result": string(resp)}
			if err != nil {
				content["error"] = err.Error()
			}
			for key, value := range content {
				if value, ok := value.(string); ok && len([]rune(value)) > 2048 {
					content[key] = string([]rune(value)[:2047]) + "…"
				}
			}
			data, _ := json.Marshal(content)
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "hook.platform_call",
				Module:       "hook",
				Type:         globalevents.ContentJSON,
				Content:      string(data),
				ContentLimit: 4096,
			}
			record.ResultStatus = logResult.ResultStatus
			if logResult.ResultStatus == globalevents.ResultFailed {
				record.Level = slog.LevelError
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			m.Opts.Log(ctx, record)
		}
		if err != nil {
			return nil, logResult, err
		}
		var decoded any
		if len(resp) > 0 && json.Unmarshal(resp, &decoded) == nil {
			return decoded, logResult, nil
		}
		return string(resp), logResult, nil
	case "output.send":
		logHook := func(outputs []delivery.Output, receipt delivery.Receipt, logResult globalevents.LogResult) {
			content := map[string]any{"source": "rules", "rule": firstNonEmpty(action.source.FinalName, action.ActionName), "point": string(event.Point), "tool": event.Tool.Name, "output_count": len(outputs), "platform_message_ids": receipt.PlatformMessageIDs, "result": logResult.Content}
			content["sent_messages"] = []map[string]any{}
			for _, sent := range receipt.SentMessages {
				content["sent_messages"] = append(content["sent_messages"].([]map[string]any), map[string]any{"platform": sent.Platform, "scope_id": sent.ScopeID, "platform_message_id": sent.PlatformMessageID, "output_indexes": sent.OutputIndexes})
			}
			if event.Tool.ID != "" {
				content["tool_call_id"] = event.Tool.ID
			}
			record := globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, EventName: "hook_tool_log", Module: "hook", Type: globalevents.ContentJSON, ContentLimit: 0, ResultStatus: logResult.ResultStatus}
			if logResult.ResultStatus == globalevents.ResultFailed {
				record.Level = slog.LevelError
			}
			if event.Session.ID != "" {
				record.Identity.SessionID = &event.Session.ID
				if event.Actor.ID != "" {
					content["actor_id"] = event.Actor.ID
				}
			} else if actor, ok := contextinfo.ActorFromContext(ctx); ok && actor.ID != "" {
				content["actor_id"] = actor.ID
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if record.Identity.SessionID == nil && facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			for key, value := range content {
				if value, ok := value.(string); ok && len([]rune(value)) > 2048 {
					content[key] = string([]rune(value)[:2047]) + "…"
				}
			}
			data, _ := json.Marshal(content)
			record.Content = string(data)
			m.Opts.Log(ctx, record)
		}
		paramsMap, err := rawObject(params)
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "send not entered; invalid output params: " + err.Error()}
			logHook(nil, delivery.Receipt{}, logResult)
			return nil, logResult, err
		}
		outputs, err := protocolOutputsFromMap(paramsMap, action, renderedActionTarget(action, event, state), render(action.Timing, event, state), "params.outputs")
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "send not entered; output preparation: " + err.Error()}
			logHook(nil, delivery.Receipt{}, logResult)
			return nil, logResult, err
		}
		receipt, logResult, err := m.Opts.Send(ctx, delivery.Target{}, outputs)
		logHook(outputs, receipt, logResult)
		if err != nil {
			return nil, logResult, err
		}
		return map[string]any{"sent": len(outputs), "receipts": []delivery.Receipt{receipt}}, logResult, nil
	case "message.get_reply":
		return map[string]any{"message_id": event.Platform.ReplyToMessageID, "available": event.Platform.ReplyToMessageID != ""}, logResult, nil
	case "message.get":
		return map[string]any{"available": false}, logResult, nil
	case "hook.log":

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "hook_plugin_log",
				Module:       "hook",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("%s\nrule=%v", "hook plugin log"+": "+string(params), firstNonEmpty(action.source.FinalName, action.ActionName)),
				ContentLimit: 4096,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			m.Opts.Log(ctx, record)
		}

		return map[string]any{"ok": true}, logResult, nil
	default:
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("unsupported hook protocol method %q", method)}, fmt.Errorf("unsupported hook protocol method %q", method)
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
	timing, err := rawString(values["timing"])
	if err != nil {
		return nil, err
	}
	return hookoutput.BuildGroup(hookoutput.Group{Outputs: specs, Target: target, Timing: timing}, hookoutput.BuildOptions{BaseDir: action.sourceBaseDir(), DefaultTarget: defaultTarget, DefaultTiming: defaultTiming})
}

func renderedActionTarget(action Action, event share.Event, state state) hookoutput.Target {
	return hookoutput.Target{
		Platform: render(action.Target.Platform, event, state), ScopeID: render(action.Target.ScopeID, event, state),
		PrivateUserID: render(action.Target.PrivateUserID, event, state), GroupID: render(action.Target.GroupID, event, state), Superadmins: action.Target.Superadmins,
	}
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func rawString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
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
	return fmt.Errorf("write hook plugin stdin %s frame failed; plugin may have exited early or closed stdin: %w", frame, err)
}

func execProcessError(err error) error {
	return fmt.Errorf("hook plugin exec failed: %w", err)
}

func execContextError(ctx context.Context, action Action) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("hook plugin exec canceled: %w", context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if action.TimeoutSeconds > 0 {
			return fmt.Errorf("hook plugin exec timed out after %ds: %w", action.TimeoutSeconds, context.DeadlineExceeded)
		}
		return context.DeadlineExceeded
	}
	return nil
}

func withExecStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
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
	ctx     context.Context
	mu      sync.Mutex
	module  Module
	action  Action
	tail    *execStderrTail
	pending strings.Builder
}

func newExecStderrLogger(ctx context.Context, module Module, action Action, tail *execStderrTail) *execStderrLogger {
	return &execStderrLogger{ctx: ctx, module: module, action: action, tail: tail}
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
	w.tail.Add(line)
	if line != "" {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "hook_exec_stderr",
				Module:       "hook",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("%s\nrule=%v", "hook exec stderr"+": "+line, firstNonEmpty(w.action.source.FinalName, w.action.ActionName)),
				ContentLimit: 4096,
			}
			facts, _ := contextinfo.ExecutionFromContext(w.ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			w.module.Opts.Log(w.ctx, record)
		}
	}
}

func (m Module) execCwd(action Action, event share.Event, state state) (string, error) {
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
