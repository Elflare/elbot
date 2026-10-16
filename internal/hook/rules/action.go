package rules

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type state struct {
	Actions map[string]actionResult
}

type actionResult struct {
	Result      string
	Error       string
	Matched     *bool
	PassThrough *bool
}

func (m Module) runAction(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, globalevents.LogResult, error) {
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "action=" + action.Type}
	switch strings.TrimSpace(action.Type) {
	case "prepend", "append", "replace", "delete":
		updated, err := applyTextAction(event, action, state)
		if err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		}
		return updated, actionResult{}, logResult, err
	case "send":
		outputs, err := makeOutputs(action, event, state)
		if err != nil {
			return event, actionResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "output preparation failed; send not entered: " + err.Error()}, err
		}
		event.Outputs = append(event.Outputs, outputs...)
		logResult.Content = "outputs prepared for business delivery"
		return event, actionResult{}, logResult, nil
	case "exec":
		updated, result, logResult, err := m.runExec(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.ActionName), "exec")
		state.Actions[key] = result
		return updated, result, logResult, err
	case "tool":
		result, logResult, err := m.callTool(ctx, event, action, state)
		// The invocation has its own audit record; the rule reports only its action stage.
		logResult.Content = "tool action: " + action.Tool + " status=" + logResult.ResultStatus.String()
		if err != nil {
			logResult.Content += "; business_error=" + err.Error()
		}
		key := firstNonEmpty(strings.TrimSpace(action.ActionName), strings.TrimSpace(action.Tool))
		if key != "" {
			state.Actions[key] = result
		}
		return event, result, logResult, err
	default:
		err := fmt.Errorf("unsupported action type %q", action.Type)
		return event, actionResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
}

func applyTextAction(event hook.Event, action Action, state state) (hook.Event, error) {
	field := strings.TrimSpace(action.Field)
	if err := allowField(event, field); err != nil {
		return event, err
	}
	switch strings.TrimSpace(action.Type) {
	case "prepend":
		return prependTextField(event, field, render(action.Text, event, state)), nil
	case "append":
		return appendTextField(event, field, render(action.Text, event, state)), nil
	case "replace":
		pattern, err := regexp.Compile(render(action.Match, event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, render(action.Replace, event, state), action.All), nil
	case "delete":
		pattern, err := regexp.Compile(render(firstNonEmpty(action.Match, action.Text), event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, "", true), nil
	default:
		return event, fmt.Errorf("unsupported text action %q", action.Type)
	}
}

func prependTextField(event hook.Event, field, value string) hook.Event {
	switch field {
	case "message.text":
		event.Message.Segments = llm.PrependSegmentText(event.Message.Segments, value)
	case "llm.latest_user_text":
		event.Message.Segments = llm.PrependSegmentText(event.Message.Segments, value)
	case "llm.text":
		event.LLM.Text = value + event.LLM.Text
	case "tool.arguments":
		event.Tool.Arguments = value + event.Tool.Arguments
	case "tool.result":
		event.Message.Segments = llm.PrependSegmentText(toolResultSegments(event), value)
		event.Tool.Result = llm.SegmentsTextOnly(event.Message.Segments)
	}
	return event
}

func appendTextField(event hook.Event, field, value string) hook.Event {
	switch field {
	case "message.text":
		event.Message.Segments = llm.AppendSegmentText(event.Message.Segments, value)
	case "llm.latest_user_text":
		event.Message.Segments = llm.AppendSegmentText(event.Message.Segments, value)
	case "llm.text":
		event.LLM.Text += value
	case "tool.arguments":
		event.Tool.Arguments += value
	case "tool.result":
		event.Message.Segments = llm.AppendSegmentText(toolResultSegments(event), value)
		event.Tool.Result = llm.SegmentsTextOnly(event.Message.Segments)
	}
	return event
}

func replaceTextField(event hook.Event, field string, pattern *regexp.Regexp, replacement string, all bool) hook.Event {
	switch field {
	case "message.text":
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, pattern, replacement, all)
	case "llm.latest_user_text":
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, pattern, replacement, all)
	case "llm.text":
		event.LLM.Text = replaceString(event.LLM.Text, pattern, replacement, all)
	case "tool.arguments":
		event.Tool.Arguments = replaceString(event.Tool.Arguments, pattern, replacement, all)
	case "tool.result":
		event.Message.Segments = llm.ReplaceSegmentText(toolResultSegments(event), pattern, replacement, all)
		event.Tool.Result = llm.SegmentsTextOnly(event.Message.Segments)
	}
	return event
}

func replaceString(text string, pattern *regexp.Regexp, replacement string, all bool) string {
	if all {
		return pattern.ReplaceAllString(text, replacement)
	}
	loc := pattern.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + pattern.ReplaceAllString(text[loc[0]:loc[1]], replacement) + text[loc[1]:]
}

func toolResultSegments(event hook.Event) []llm.MessageSegment {
	if len(event.Message.Segments) > 0 {
		return event.Message.Segments
	}
	return llm.TextSegments(event.Tool.Result)
}

func allowField(event hook.Event, field string) error {
	switch event.Point {
	case hook.PointPlatformMessageReceived, hook.PointAgentInputPrepared:
		if field == "message.text" {
			return nil
		}
	case hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared:
		if field == "llm.latest_user_text" {
			return nil
		}
	case hook.PointLLMResponseReceived:
		if field == "llm.text" {
			return nil
		}
	case hook.PointToolCallPrepared:
		if field == "tool.arguments" {
			return nil
		}
	case hook.PointToolCallCompleted:
		if field == "tool.result" {
			return nil
		}
	case hook.PointAgentOutputPrepared, hook.PointAgentTurnOutputPrepared, hook.PointPlatformMessageSent:
		if field == "message.text" && event.Message.Role == string(llm.RoleAssistant) {
			return nil
		}

	}
	return fmt.Errorf("field %q cannot be edited at hook point %q", field, event.Point)
}

func makeOutputs(action Action, event hook.Event, state state) ([]delivery.Output, error) {
	target := hookoutput.Target{
		Platform: render(action.Target.Platform, event, state), ScopeID: render(action.Target.ScopeID, event, state),
		PrivateUserID: render(action.Target.PrivateUserID, event, state), GroupID: render(action.Target.GroupID, event, state), Superadmins: action.Target.Superadmins,
	}
	if strings.TrimSpace(target.Platform) == "" && event.Point == hook.PointPlatformConnected {
		target.Platform = event.Platform.Name
	}
	quick := SegmentSpec{Kind: action.Kind, Text: action.Text, URL: action.URL, Path: action.Path, Base64: action.Base64, Name: action.Name, MIMEType: action.MIMEType, UserID: action.UserID, MessageID: action.MessageID, EmoticonID: action.EmoticonID}
	if len(action.Outputs) > 0 && segmentSpecSet(quick) {
		return nil, fmt.Errorf("send action cannot combine outputs with quick output fields")
	}
	specs := action.Outputs
	if len(specs) == 0 {
		specs = []SegmentSpec{quick}
	}
	rendered := make([]SegmentSpec, 0, len(specs))
	for _, seg := range specs {
		rendered = append(rendered, SegmentSpec{
			Kind: render(seg.Kind, event, state), Text: render(seg.Text, event, state), URL: render(seg.URL, event, state), Path: render(seg.Path, event, state),
			Base64: render(seg.Base64, event, state), Name: render(seg.Name, event, state), MIMEType: render(seg.MIMEType, event, state),
			UserID: render(seg.UserID, event, state), MessageID: render(seg.MessageID, event, state), EmoticonID: render(seg.EmoticonID, event, state),
		})
	}
	return hookoutput.BuildGroup(hookoutput.Group{Outputs: rendered, Target: target, Timing: render(action.Timing, event, state)}, hookoutput.BuildOptions{BaseDir: action.sourceBaseDir()})
}

func segmentSpecSet(spec SegmentSpec) bool {
	return spec.Kind != "" || spec.Text != "" || spec.URL != "" || spec.Path != "" || spec.Base64 != "" || spec.Name != "" || spec.MIMEType != "" || spec.UserID != "" || spec.MessageID != "" || spec.EmoticonID != ""
}

// callTool invokes a registered tool without Agent risk or confirmation gates.
// Hook authors own authorization and risk decisions for Hook-triggered calls.
func (m Module) callTool(ctx context.Context, event hook.Event, action Action, state state) (actionResult, globalevents.LogResult, error) {
	name := strings.TrimSpace(action.Tool)
	arguments := render(action.Arguments, event, state)
	call := llm.ToolCallRequest{ID: storage.NewID(), Name: name, Arguments: arguments}
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, EventName: "hook_tool_call_completed", Module: "hook", Type: globalevents.ContentJSON, ContentLimit: 0}
	content := struct {
		Tool       string  `json:"tool"`
		ActorID    string  `json:"actor_id,omitempty"`
		Arguments  string  `json:"arguments,omitempty"`
		Point      string  `json:"point"`
		Result     *string `json:"result,omitempty"`
		ElapsedMS  *int64  `json:"elapsed_ms,omitempty"`
		Source     string  `json:"source"`
		ToolCallID string  `json:"tool_call_id"`
	}{
		Tool:       tool.TruncateLogContent(name, 2048),
		Arguments:  tool.TruncateLogContent(arguments, 512),
		Point:      tool.TruncateLogContent(string(event.Point), 2048),
		Source:     "rules",
		ToolCallID: call.ID,
	}
	if event.Session.ID != "" {
		if event.Actor.ID != "" {
			content.ActorID = tool.TruncateLogContent(event.Actor.ID, 2048)
		}
		record.Identity.SessionID = &event.Session.ID
	} else {
		if actor, ok := contextinfo.ActorFromContext(ctx); ok && actor.ID != "" {
			content.ActorID = tool.TruncateLogContent(actor.ID, 2048)
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
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
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
	var toolResult *tool.Result
	var err error
	switch {
	case name == "":
		err = fmt.Errorf("tool is required")
		logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "Tool.Call not entered: " + err.Error()}
	default:
		registered, ok := m.Opts.Tools.Get(name)
		if !ok {
			err = fmt.Errorf("tool %q not found", name)
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "Tool.Call not entered: " + err.Error()}
		} else if err = ctx.Err(); err != nil {
			logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "Tool.Call not entered: " + err.Error()}
			if err == context.Canceled {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		} else {
			started := time.Now()
			data, _ := json.Marshal(content)
			start := record
			start.EventName, start.Content, start.ResultStatus = "hook_tool_call_prepared", string(data), globalevents.ResultSucceeded
			hook.LogHook(ctx, start)
			toolResult, logResult.Content, err = registered.Call(ctx, tool.CallRequest{ID: call.ID, Name: name, Arguments: json.RawMessage(arguments)})
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				if errors.Is(err, context.Canceled) {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			elapsed := time.Since(started).Milliseconds()
			content.ElapsedMS = &elapsed
		}
	}
	record.ResultStatus = logResult.ResultStatus
	if logResult.ResultStatus == globalevents.ResultFailed {
		record.Level = slog.LevelError
	}
	content.Result = &logResult.Content
	content.Arguments = ""
	data, _ := json.Marshal(content)
	record.Content = string(data)
	hook.LogHook(ctx, record)
	if err != nil {
		return actionResult{Error: err.Error()}, logResult, err
	}
	if toolResult != nil {
		return actionResult{Result: llm.SegmentsContentText(toolResult.LLMSegments())}, logResult, nil
	}
	return actionResult{}, logResult, nil
}

func render(text string, event hook.Event, state state) string {
	replacements := hook.TemplateValues(event)
	for index, match := range hook.EventMatchContext(event).Regex {
		prefix := fmt.Sprintf("{{match.regex.%d", index)
		replacements[prefix+".text}}"] = match.Text
		replacements[prefix+".field}}"] = match.Field
		replacements[prefix+".pattern}}"] = match.Value
		for groupIndex, group := range match.Groups {
			replacements[fmt.Sprintf("{{match.regex.%d.group.%d}}", index, groupIndex)] = group
		}
		for name, group := range match.Named {
			replacements[fmt.Sprintf("{{match.regex.%d.%s}}", index, name)] = group
		}
	}
	for name, result := range state.Actions {
		replacements["{{actions."+name+".result}}"] = result.Result
		replacements["{{actions."+name+".error}}"] = result.Error
	}
	for old, newText := range replacements {
		text = strings.ReplaceAll(text, old, newText)
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func byteSize(bytes int) string {
	if bytes%(1024*1024) == 0 {
		return fmt.Sprintf("%d MiB", bytes/(1024*1024))
	}
	if bytes%1024 == 0 {
		return fmt.Sprintf("%d KiB", bytes/1024)
	}
	return fmt.Sprintf("%d bytes", bytes)
}

// setTextField overwrites a text field with the given value, using allowField for permission checks.
func setTextField(event hook.Event, field, value string) (hook.Event, error) {
	field = strings.TrimSpace(field)
	if err := allowField(event, field); err != nil {
		return event, err
	}
	switch field {
	case "message.text":
		event.Message.Segments = llm.SetSegmentText(event.Message.Segments, value)
	case "llm.latest_user_text":
		event.Message.Segments = llm.SetSegmentText(event.Message.Segments, value)
	case "llm.text":
		event.LLM.Text = value
	case "tool.arguments":
		event.Tool.Arguments = value
	case "tool.result":
		event.Tool.Result = value
		event.Message.Segments = llm.SetSegmentText(toolResultSegments(event), value)
	}
	return event, nil
}
