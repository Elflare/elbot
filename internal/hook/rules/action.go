package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
	"elbot/internal/llm"
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

func (m Module) runAction(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, actionResult, error) {
	switch strings.TrimSpace(action.Type) {
	case "prepend", "append", "replace", "delete":
		updated, err := applyTextAction(event, action, state)
		return updated, actionResult{}, err
	case "send":
		outputs, err := makeOutputs(action, event, state)
		if err != nil {
			return event, actionResult{}, err
		}
		event.Outputs = append(event.Outputs, outputs...)
		return event, actionResult{}, nil
	case "exec":
		updated, result, err := m.runExec(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.ActionName), "exec")
		if key != "" {
			state.Actions[key] = result
		}
		return updated, result, err
	case "tool":
		result, err := m.callTool(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.ActionName), strings.TrimSpace(action.Tool))
		if key != "" {
			state.Actions[key] = result
		}
		return event, result, err
	default:
		return event, actionResult{}, fmt.Errorf("unsupported action type %q", action.Type)
	}
}

func applyTextAction(event hook.Event, action Action, state state) (hook.Event, error) {
	field := strings.TrimSpace(action.Field)
	if err := allowField(event, field); err != nil {
		return event, err
	}
	switch strings.TrimSpace(action.Type) {
	case "prepend":
		return prependTextField(event, field, render(action.Text, event, state))
	case "append":
		return appendTextField(event, field, render(action.Text, event, state))
	case "replace":
		pattern, err := regexp.Compile(render(action.Match, event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, render(action.Replace, event, state), action.All)
	case "delete":
		pattern, err := regexp.Compile(render(firstNonEmpty(action.Match, action.Text), event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, "", true)
	default:
		return event, fmt.Errorf("unsupported text action %q", action.Type)
	}
}

func prependTextField(event hook.Event, field, value string) (hook.Event, error) {
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
	default:
		return event, fmt.Errorf("unsupported prepend field %q", field)
	}
	return event, nil
}

func appendTextField(event hook.Event, field, value string) (hook.Event, error) {
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
	default:
		return event, fmt.Errorf("unsupported append field %q", field)
	}
	return event, nil
}

func replaceTextField(event hook.Event, field string, pattern *regexp.Regexp, replacement string, all bool) (hook.Event, error) {
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
	default:
		return event, fmt.Errorf("unsupported replace field %q", field)
	}
	return event, nil
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
func (m Module) callTool(ctx context.Context, event hook.Event, action Action, state state) (actionResult, error) {
	if m.Opts.Tools == nil {
		return actionResult{Error: "tool registry is not configured"}, fmt.Errorf("tool registry is not configured")
	}
	name := strings.TrimSpace(action.Tool)
	if name == "" {
		return actionResult{Error: "tool is required"}, fmt.Errorf("tool is required")
	}
	arguments := render(action.Arguments, event, state)
	call := llm.ToolCallRequest{ID: "hook:" + name, Name: name, Arguments: arguments}
	registered, ok := m.Opts.Tools.Get(name)
	if !ok {
		return actionResult{Error: "tool not found"}, fmt.Errorf("tool %q not found", name)
	}
	toolResult, err := registered.Call(ctx, tool.CallRequest{ID: call.ID, Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil {
		m.audit("hook_tool_error", "tool", name, "error", err.Error())
		return actionResult{Error: err.Error()}, err
	}
	content := ""
	if toolResult != nil {
		content = llm.SegmentsContentText(toolResult.LLMSegments())
	}
	m.audit("hook_tool_call", "tool", name)
	return actionResult{Result: content}, nil
}

func (m Module) audit(event string, attrs ...any) {
	if m.Opts.Audit != nil {
		m.Opts.Audit(event, attrs...)
	}
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
	default:
		return event, fmt.Errorf("unsupported set field %q", field)
	}
	return event, nil
}
