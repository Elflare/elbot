// 实现 Hook 的文本改写、输出准备和工具调用等动作。

package rules

import "elbot/internal/tool"

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
	hookoutput "elbot/internal/hook/share/output"
	"elbot/internal/llm"
	"elbot/internal/storage"
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

func (m Module) runAction(ctx context.Context, event share.Event, action Action, state state) (share.Event, actionResult, error) {

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
		state.Actions[key] = result
		return updated, result, err
	case "tool":
		result, err := m.callTool(ctx, event, action, state)
		// The invocation has its own audit record; the rule reports only its action stage.
		key := firstNonEmpty(strings.TrimSpace(action.ActionName), strings.TrimSpace(action.Tool))
		if key != "" {
			state.Actions[key] = result
		}
		return event, result, err
	default:
		err := fmt.Errorf("unsupported action type %q", action.Type)
		return event, actionResult{}, err
	}
}

func applyTextAction(event share.Event, action Action, state state) (share.Event, error) {
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
		pattern, err := regexp.Compile(render(action.Pattern, event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, render(action.Replace, event, state), action.All), nil
	case "delete":
		pattern, err := regexp.Compile(render(firstNonEmpty(action.Pattern, action.Text), event, state))
		if err != nil {
			return event, err
		}
		return replaceTextField(event, field, pattern, "", true), nil
	default:
		return event, fmt.Errorf("unsupported text action %q", action.Type)
	}
}

func prependTextField(event share.Event, field, value string) share.Event {
	switch field {
	case "message.text", "llm.latest_user_text":
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

func appendTextField(event share.Event, field, value string) share.Event {
	switch field {
	case "message.text", "llm.latest_user_text":
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

func replaceTextField(event share.Event, field string, pattern *regexp.Regexp, replacement string, all bool) share.Event {
	switch field {
	case "message.text", "llm.latest_user_text":
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

func toolResultSegments(event share.Event) []llm.MessageSegment {
	if len(event.Message.Segments) > 0 {
		return event.Message.Segments
	}
	return llm.TextSegments(event.Tool.Result)
}

func allowField(event share.Event, field string) error {
	switch event.Point {
	case share.PointPlatformMessageReceived, share.PointAgentInputPrepared:
		if field == "message.text" {
			return nil
		}
	case share.PointLLMTurnPrepared, share.PointLLMRequestPrepared:
		if field == "llm.latest_user_text" {
			return nil
		}
	case share.PointLLMResponseReceived:
		if field == "llm.text" {
			return nil
		}
	case share.PointToolCallPrepared:
		if field == "tool.arguments" {
			return nil
		}
	case share.PointToolCallCompleted:
		if field == "tool.result" {
			return nil
		}
	case share.PointAgentOutputPrepared, share.PointAgentTurnOutputPrepared, share.PointPlatformMessageSent:
		if field == "message.text" && event.Message.Role == string(llm.RoleAssistant) {
			return nil
		}

	}
	return fmt.Errorf("field %q cannot be edited at hook point %q", field, event.Point)
}

func makeOutputs(action Action, event share.Event, state state) ([]delivery.Output, error) {
	target := renderedActionTarget(action, event, state)
	if strings.TrimSpace(target.Platform) == "" && event.Point == share.PointPlatformConnected {
		target.Platform = event.Chat.Platform
	}
	quick := SegmentSpec{Kind: action.Kind, Text: action.Text, URL: action.URL, Path: action.Path, Base64: action.Base64, Name: action.Name, MIMEType: action.MIMEType, UserID: action.UserID, MessageID: action.MessageID, EmoticonID: action.EmoticonID}
	if len(action.Outputs) > 0 && quick != (SegmentSpec{}) {
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

// callTool invokes a registered tool without Agent risk or confirmation gates.
// Hook authors own authorization and risk decisions for Hook-triggered calls.
func (m Module) callTool(ctx context.Context, event share.Event, action Action, state state) (actionResult, error) {
	name := strings.TrimSpace(action.Tool)
	arguments := render(action.Arguments, event, state)
	call := storage.NewID()
	record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Level: slog.LevelInfo, EventName: "hook_tool_call_completed", Module: "hook", Type: globalevents.ContentJSON, ContentLimit: 0}
	content := struct {
		Tool       string             `json:"tool"`
		Actor      share.ActorContext `json:"actor"`
		Arguments  string             `json:"arguments,omitempty"`
		Point      string             `json:"point"`
		Summary    *string            `json:"summary,omitempty"` // Source-owned summary, preserved without truncation.
		Result     *string            `json:"result,omitempty"`
		ElapsedMS  *int64             `json:"elapsed_ms,omitempty"`
		Source     string             `json:"source"`
		ToolCallID string             `json:"tool_call_id"`
	}{
		Tool:       share.TruncateLogContent(name, 2048),
		Actor:      event.Actor,
		Arguments:  share.TruncateLogContent(arguments, 512),
		Point:      share.TruncateLogContent(string(event.Point), 2048),
		Source:     "rules",
		ToolCallID: call,
	}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	record.Identity.SessionID = event.Session.ID
	summary := ""
	record.ResultStatus = globalevents.ResultSucceeded
	var toolResult *tool.Result
	var err error
	switch {
	case name == "":
		err = fmt.Errorf("tool is required")
		summary = "Tool.Call not entered: " + err.Error()
		record.Level, record.ResultStatus = slog.LevelWarn, globalevents.ResultFailed
	default:
		started := time.Now()
		data, _ := json.Marshal(content)
		start := record
		// 此记录表示开始调用 Tool 服务，不代表工具已准备或执行成功。
		start.EventName, start.Content, start.ResultStatus = "hook_tool_call_prepared", string(data), globalevents.ResultSucceeded
		m.Opts.Log(ctx, start)
		toolResult, summary, err = m.Opts.Tools.CallTool(ctx, tool.CallRequest{ID: call, Name: name, Arguments: json.RawMessage(arguments)})
		if err != nil {
			record.ResultStatus = globalevents.ResultFailed
			if globalevents.LogStatus(err) == globalevents.ResultCanceled {
				record.ResultStatus = globalevents.ResultCanceled
			}
		}
		elapsed := time.Since(started).Milliseconds()
		content.ElapsedMS = &elapsed
	}
	if record.ResultStatus == globalevents.ResultFailed {
		record.Level = slog.LevelWarn
	}
	content.Summary = &summary
	body := toolResult.LogContent(err)
	content.Result = &body
	content.Arguments = ""
	data, _ := json.Marshal(content)
	record.Content = string(data)
	m.Opts.Log(ctx, record)
	if err != nil {
		return actionResult{Error: err.Error()}, err
	}
	if toolResult != nil {
		return actionResult{Result: llm.SegmentsContentText(toolResult.LLMSegments())}, nil
	}
	return actionResult{}, nil
}

func render(text string, event share.Event, state state) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	replacements := share.TemplateValues(event)
	for index, match := range share.EventMatchContext(event).Regex {
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
func setTextField(event share.Event, field, value string) (share.Event, error) {
	field = strings.TrimSpace(field)
	if err := allowField(event, field); err != nil {
		return event, err
	}
	switch field {
	case "message.text", "llm.latest_user_text":
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
