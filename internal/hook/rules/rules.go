package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/output"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const (
	ConfigFile      = "hooks.toml"
	DefaultPriority = 1000
)

type Options struct {
	ConfigDir string
	Tools     *tool.Registry
	Policy    *security.Policy
	Logger    *slog.Logger
	Audit     func(event string, attrs ...any)
	Notify    func(context.Context, string)
}

type Config struct {
	Rules []Rule `toml:"rules"`
}

type Rule struct {
	Name     string           `toml:"name"`
	On       string           `toml:"on"`
	Priority int              `toml:"priority"`
	Enabled  *bool            `toml:"enabled"`
	If       string           `toml:"if"`
	Op       string           `toml:"op"`
	Value    string           `toml:"value"`
	Always   bool             `toml:"always"`
	Match    []hook.Condition `toml:"match"`
	Action   string           `toml:"action"`
	Actions  []Action         `toml:"actions"`
	Field    string           `toml:"field"`
	Text     string           `toml:"text"`
	Pattern  string           `toml:"pattern"`
	Replace  string           `toml:"replace"`
	Kind     string           `toml:"kind"`
	Path     string           `toml:"path"`
	Tool     string           `toml:"tool"`
	Args     string           `toml:"arguments"`
	All      bool             `toml:"all"`
	Target   Target           `toml:"target"`
}

type Action struct {
	Name      string `toml:"name"`
	Type      string `toml:"type"`
	Field     string `toml:"field"`
	Text      string `toml:"text"`
	Pattern   string `toml:"pattern"`
	Match     string `toml:"-"`
	Replace   string `toml:"replace"`
	Kind      string `toml:"kind"`
	Path      string `toml:"path"`
	Tool      string `toml:"tool"`
	Arguments string `toml:"arguments"`
	All       bool   `toml:"all"`
	Target    Target `toml:"target"`
}

type Target struct {
	Platform      string `toml:"platform"`
	ScopeID       string `toml:"scope_id"`
	PrivateUserID string `toml:"private_user_id"`
	GroupID       string `toml:"group_id"`
	Superadmins   bool   `toml:"superadmins"`
}

type Module struct {
	Rules  []Rule
	Opts   Options
	Logger *slog.Logger
}

func NewModule(opts Options) (Module, error) {
	cfg, path, err := loadConfig(opts.ConfigDir)
	if err != nil {
		reportConfigError(context.Background(), opts, path, err)
		return Module{}, err
	}

	module := Module{Rules: cfg.Rules, Opts: opts, Logger: opts.Logger}
	if module.Logger != nil {
		enabled := 0
		for _, rule := range module.Rules {
			if rule.enabled() {
				enabled++
			}
		}
		module.Logger.Info("hook rule config loaded", "path", path, "rules", len(module.Rules), "enabled", enabled)
	}
	return module, nil
}

func (m Module) RegisterHooks(registrar hook.Registrar) error {
	for index, rule := range m.Rules {
		if !rule.enabled() {
			continue
		}
		if err := validateRule(rule); err != nil {
			return err
		}
		priority := rule.Priority
		if priority == 0 {
			priority = DefaultPriority
		}
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = fmt.Sprintf("rule.%d", index+1)
		}
		if m.Logger != nil {
			m.Logger.Info("hook rule registered", "name", name, "point", rule.On, "priority", priority, "matches", len(rule.Match), "actions", len(rule.Actions))
		}

		rule := rule
		if err := registrar.Register(hook.Registration{
			Point:    hook.Point(rule.On),
			Priority: priority,
			Name:     "rules." + name,
			Match:    hook.Match{Conditions: rule.Match},
			Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
				return m.runRule(ctx, rule, event)
			}),
		}); err != nil {
			return err
		}
	}
	return nil
}

func loadConfig(configDir string) (Config, string, error) {
	if strings.TrimSpace(configDir) == "" {
		return Config{}, "", nil
	}
	path := filepath.Join(configDir, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, path, nil
		}
		return Config{}, path, fmt.Errorf("read hook rule config %q: %w", path, err)
	}
	var cfg Config
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	return cfg, path, nil
}

func reportConfigError(ctx context.Context, opts Options, path string, err error) {
	if opts.Logger != nil {
		opts.Logger.Error("hook rule config error", "path", path, "error", err)
	}
	if opts.Notify != nil {
		opts.Notify(ctx, fmt.Sprintf("Hook rules 配置错误：%v", err))
	}
}

func (c *Config) normalize() error {
	for i := range c.Rules {
		if err := c.Rules[i].normalize(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func (r *Rule) normalize() error {
	conditions, err := r.conditions()
	if err != nil {
		return err
	}
	r.Match = conditions

	for i := range r.Actions {
		r.Actions[i].normalize()
	}
	if strings.TrimSpace(r.Action) == "" {
		return nil
	}
	if len(r.Actions) > 0 {
		return fmt.Errorf("action cannot be combined with actions")
	}
	r.Actions = []Action{r.inlineAction()}
	return nil
}

func (r Rule) conditions() ([]hook.Condition, error) {
	flat := strings.TrimSpace(r.If) != "" || strings.TrimSpace(r.Op) != "" || r.Value != ""
	if r.Always {
		if flat || len(r.Match) > 0 {
			return nil, fmt.Errorf("always cannot be combined with if/op/value or match")
		}
		return []hook.Condition{{Op: hook.MatchAlways}}, nil
	}
	if flat && len(r.Match) > 0 {
		return nil, fmt.Errorf("if/op/value cannot be combined with match")
	}
	if flat {
		return []hook.Condition{{Field: r.If, Op: r.Op, Value: r.Value}}, nil
	}
	return r.Match, nil
}

func (r Rule) inlineAction() Action {
	action := Action{
		Type:      r.Action,
		Field:     r.Field,
		Text:      r.Text,
		Pattern:   r.Pattern,
		Replace:   r.Replace,
		Kind:      r.Kind,
		Path:      r.Path,
		Tool:      r.Tool,
		Arguments: r.Args,
		All:       r.All,
		Target:    r.Target,
	}
	action.normalize()
	return action
}

func (a *Action) normalize() {
	if a.Match == "" {
		a.Match = a.Pattern
	}
}

func (r Rule) enabled() bool {
	return r.Enabled == nil || *r.Enabled
}

func validateRule(rule Rule) error {
	if strings.TrimSpace(rule.On) == "" {
		return fmt.Errorf("hook rule %q missing on", rule.Name)
	}
	if !hook.KnownPoint(hook.Point(rule.On)) {
		return fmt.Errorf("hook rule %q unknown hook point %q", rule.Name, rule.On)
	}

	if len(rule.Actions) == 0 {
		return fmt.Errorf("hook rule %q has no actions", rule.Name)
	}
	if len(rule.Match) == 0 {
		return fmt.Errorf("hook rule %q missing match", rule.Name)
	}
	if err := (hook.Match{Conditions: rule.Match}).Validate(); err != nil {
		return fmt.Errorf("hook rule %q matches: %w", rule.Name, err)
	}

	for _, action := range rule.Actions {
		if strings.TrimSpace(action.Type) == "" {
			return fmt.Errorf("hook rule %q has action without type", rule.Name)
		}
	}
	return nil
}

func (m Module) runRule(ctx context.Context, rule Rule, event hook.Event) (hook.Event, error) {
	state := state{Actions: map[string]actionResult{}}
	var err error
	for index, action := range rule.Actions {
		event, err = m.runAction(ctx, event, action, state)
		if err != nil {
			return event, fmt.Errorf("rule %q action %d %s: %w", rule.Name, index+1, action.Type, err)
		}
	}
	return event, nil
}

type state struct {
	Actions map[string]actionResult
}

type actionResult struct {
	Result string
	Error  string
}

func (m Module) runAction(ctx context.Context, event hook.Event, action Action, state state) (hook.Event, error) {
	switch strings.TrimSpace(action.Type) {
	case "prepend", "append", "replace", "delete":
		return applyTextAction(event, action, state)
	case "send":
		out, err := makeOutput(action, event, state)
		if err != nil {
			return event, err
		}
		event.Outputs = append(event.Outputs, out)
		return event, nil
	case "tool":
		result, err := m.callTool(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.Name), strings.TrimSpace(action.Tool))
		if key != "" {
			state.Actions[key] = result
		}
		return event, err
	default:
		return event, fmt.Errorf("unsupported action type %q", action.Type)
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

func readTextField(event hook.Event, field string) string {
	switch strings.TrimSpace(field) {
	case "message.text":
		return llm.SegmentsTextOnly(event.Message.Segments)
	case "message.content_text":
		return llm.SegmentsContentText(event.Message.Segments)
	case "llm.text":
		return event.LLM.Text
	case "llm.raw_text":
		return event.LLM.RawText
	case "llm.latest_user_text":
		return llm.LatestUserSegmentTextOnly(event.LLM.Messages)
	case "llm.latest_user_content_text":
		return llm.LatestUserSegmentContentText(event.LLM.Messages)
	case "tool.arguments":
		return event.Tool.Arguments
	case "tool.result":
		return event.Tool.Result
	default:
		return ""
	}
}

func prependTextField(event hook.Event, field, value string) (hook.Event, error) {
	switch field {
	case "message.text":
		event.Message.Segments = llm.PrependSegmentText(event.Message.Segments, value)
	case "llm.latest_user_text":
		event.LLM.Messages = llm.PrependLatestUserSegmentText(event.LLM.Messages, value)
	case "llm.text":
		event.LLM.Text = value + event.LLM.Text
	case "tool.arguments":
		event.Tool.Arguments = value + event.Tool.Arguments
	case "tool.result":
		event.Tool.Result = value + event.Tool.Result
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
		event.LLM.Messages = llm.AppendLatestUserSegmentText(event.LLM.Messages, value)
	case "llm.text":
		event.LLM.Text += value
	case "tool.arguments":
		event.Tool.Arguments += value
	case "tool.result":
		event.Tool.Result += value
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
		event.LLM.Messages = llm.ReplaceLatestUserSegmentText(event.LLM.Messages, pattern, replacement, all)
	case "llm.text":
		event.LLM.Text = replaceString(event.LLM.Text, pattern, replacement, all)
	case "tool.arguments":
		event.Tool.Arguments = replaceString(event.Tool.Arguments, pattern, replacement, all)
	case "tool.result":
		event.Tool.Result = replaceString(event.Tool.Result, pattern, replacement, all)
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

func makeOutput(action Action, event hook.Event, state state) (output.Output, error) {
	kind := output.Kind(strings.TrimSpace(action.Kind))
	if kind == "" {
		kind = output.KindText
	}
	text := render(action.Text, event, state)
	path := render(action.Path, event, state)
	var out output.Output
	switch kind {
	case output.KindText:
		out = output.Text(text)
	case output.KindImage:
		out = output.ImagePath(path)
		out.Text = text
	case output.KindFile:
		out = output.FilePath(path)
		out.Text = text
	case output.KindEmoticon:
		out = output.Emoticon(text)
		out.Source.Path = path
	case output.KindAt:
		out = output.At(text)
	default:
		return output.Output{}, fmt.Errorf("unsupported output kind %q", kind)
	}
	out.Target = output.Target{
		Platform:      render(action.Target.Platform, event, state),
		ScopeID:       render(action.Target.ScopeID, event, state),
		PrivateUserID: render(action.Target.PrivateUserID, event, state),
		GroupID:       render(action.Target.GroupID, event, state),
		Superadmins:   action.Target.Superadmins,
	}
	if strings.TrimSpace(out.Target.Platform) == "" && event.Point == hook.PointPlatformConnected {
		out.Target.Platform = event.Platform.Name
	}
	return out, nil
}

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
	t, ok := m.Opts.Tools.Get(name)
	if !ok {
		return actionResult{Error: "tool not found"}, fmt.Errorf("tool %q not found", name)
	}
	assessment, err := tool.AssessRisk(ctx, t, tool.CallRequest{ID: call.ID, Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil {
		return actionResult{Error: err.Error()}, err
	}
	actor := security.Actor{ID: event.Actor.ID, PlatformUserID: event.Actor.UserID, Role: security.RoleUser}
	if event.Actor.Role == string(security.RoleSuperadmin) {
		actor.Role = security.RoleSuperadmin
	}
	policy := m.Opts.Policy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	if !policy.CanUseTool(actor, assessment.Level) {
		err := fmt.Errorf("risk %s is above allowed tool level", assessment.Level)
		m.audit("hook_tool_denied", "tool", name, "risk", assessment.Level, "reason", err.Error())
		return actionResult{Error: err.Error()}, err
	}
	if policy.NeedsToolConfirmation(actor, assessment.Level) {
		err := fmt.Errorf("risk %s requires interactive confirmation", assessment.Level)
		m.audit("hook_tool_denied", "tool", name, "risk", assessment.Level, "reason", err.Error())
		return actionResult{Error: err.Error()}, err
	}
	result := tool.Executor{Registry: m.Opts.Tools, Actor: actor, Policy: policy}.Execute(ctx, call)
	content := llm.SegmentsContentText(result.Message.Segments)
	if result.Err != nil {
		m.audit("hook_tool_error", "tool", name, "risk", assessment.Level, "error", result.Err.Error())
		return actionResult{Result: content, Error: result.Err.Error()}, result.Err
	}
	m.audit("hook_tool_call", "tool", name, "risk", assessment.Level)
	return actionResult{Result: content}, nil
}

func (m Module) audit(event string, attrs ...any) {
	if m.Opts.Audit != nil {
		m.Opts.Audit(event, attrs...)
	}
}

func render(text string, event hook.Event, state state) string {
	replacements := map[string]string{
		"{{platform.name}}":                event.Platform.Name,
		"{{platform.scope_id}}":            event.Platform.ScopeID,
		"{{platform.user_id}}":             event.Platform.UserID,
		"{{actor.id}}":                     event.Actor.ID,
		"{{actor.user_id}}":                event.Actor.UserID,
		"{{message.text}}":                 llm.SegmentsTextOnly(event.Message.Segments),
		"{{message.content_text}}":         llm.SegmentsContentText(event.Message.Segments),
		"{{llm.text}}":                     event.LLM.Text,
		"{{llm.raw_text}}":                 event.LLM.RawText,
		"{{llm.latest_user_text}}":         llm.LatestUserSegmentTextOnly(event.LLM.Messages),
		"{{llm.latest_user_content_text}}": llm.LatestUserSegmentContentText(event.LLM.Messages),
		"{{tool.arguments}}":               event.Tool.Arguments,
		"{{tool.result}}":                  event.Tool.Result,
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
