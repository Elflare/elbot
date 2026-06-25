package rules

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/hook"
	"elbot/internal/llm"
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
	Elvena    elvena.Dispatcher
}

type Config struct {
	Rules []Rule `toml:"rules"`
}

type Rule struct {
	Name            string           `toml:"name"`
	On              string           `toml:"on"`
	Priority        int              `toml:"priority"`
	Enabled         *bool            `toml:"enabled"`
	If              string           `toml:"if"`
	Op              string           `toml:"op"`
	Value           string           `toml:"value"`
	Always          bool             `toml:"always"`
	Match           []hook.Condition `toml:"match"`
	Roles           []string         `toml:"roles"`
	ActorRoles      []string         `toml:"actor_roles"`
	GroupRoles      []string         `toml:"group_roles"`
	Action          string           `toml:"action"`
	Actions         []Action         `toml:"actions"`
	Field           string           `toml:"field"`
	Text            string           `toml:"text"`
	Pattern         string           `toml:"pattern"`
	Replace         string           `toml:"replace"`
	Kind            string           `toml:"kind"`
	Path            string           `toml:"path"`
	Timing          string           `toml:"timing"`
	Tool            string           `toml:"tool"`
	Args            string           `toml:"arguments"`
	Command         string           `toml:"command"`
	Cwd             string           `toml:"cwd"`
	Stdin           string           `toml:"stdin"`
	Stdout          string           `toml:"stdout"`
	TimeoutSeconds  int              `toml:"timeout_seconds"`
	All             bool             `toml:"all"`
	Target          Target           `toml:"target"`
	Consume         bool             `toml:"consume"`
	StopPropagation bool             `toml:"stop_propagation"`
}

type Action struct {
	Name           string        `toml:"name"`
	Type           string        `toml:"type"`
	Field          string        `toml:"field"`
	Text           string        `toml:"text"`
	Pattern        string        `toml:"pattern"`
	Match          string        `toml:"-"`
	Replace        string        `toml:"replace"`
	Kind           string        `toml:"kind"`
	Path           string        `toml:"path"`
	Timing         string        `toml:"timing"`
	Tool           string        `toml:"tool"`
	Arguments      string        `toml:"arguments"`
	All            bool          `toml:"all"`
	Command        string        `toml:"command"`
	Cwd            string        `toml:"cwd"`
	Stdin          string        `toml:"stdin"`
	Stdout         string        `toml:"stdout"`
	TimeoutSeconds int           `toml:"timeout_seconds"`
	Target         Target        `toml:"target"`
	Segments       []SegmentSpec `toml:"segments"`
}

type SegmentSpec struct {
	Kind     string `toml:"kind" json:"kind"`
	Text     string `toml:"text" json:"text,omitempty"`
	URL      string `toml:"url" json:"url,omitempty"`
	Path     string `toml:"path" json:"path,omitempty"`
	Base64   string `toml:"base64" json:"base64,omitempty"`
	Name     string `toml:"name" json:"name,omitempty"`
	MIMEType string `toml:"mime_type" json:"mime_type,omitempty"`
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
		registrations := ruleRegistrations(rule)
		for roleIndex, match := range registrations {
			regName := "rules." + name
			if len(registrations) > 1 {
				regName = fmt.Sprintf("%s.role.%d", regName, roleIndex+1)
			}
			if err := registrar.Register(hook.Registration{
				Point:    hook.Point(rule.On),
				Priority: priority,
				Name:     regName,
				Match:    match,
				Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
					return m.runRule(ctx, rule, event)
				}),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateExecAction(action Action) error {
	if strings.TrimSpace(action.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if action.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds cannot be negative")
	}
	return validateExecStdout(action.Stdout)
}

func ruleRegistrations(rule Rule) []hook.Match {
	roleConditions := ruleRoleConditions(rule)
	if len(roleConditions) == 0 {
		return []hook.Match{{Conditions: rule.Match}}
	}
	out := make([]hook.Match, 0, len(roleConditions))
	for _, condition := range roleConditions {
		conditions := append([]hook.Condition{}, rule.Match...)
		conditions = append(conditions, condition)
		out = append(out, hook.Match{Conditions: conditions})
	}
	return out
}

func ruleRoleConditions(rule Rule) []hook.Condition {
	seen := map[string]bool{}
	var out []hook.Condition
	add := func(field, role string) {
		role = strings.TrimSpace(role)
		if role == "" {
			return
		}
		key := field + ":" + role
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, hook.Condition{Field: field, Op: hook.MatchFull, Value: role})
	}
	for _, role := range rule.Roles {
		role = strings.TrimSpace(role)
		switch security.Role(role) {
		case security.RoleSuperadmin, security.RoleUser:
			add("actor.role", role)
		default:
			if parsed := security.ParseGroupRole(role); parsed != security.GroupRoleUnknown || role == string(security.GroupRoleUnknown) {
				add("actor.group_role", string(parsed))
			}
		}
	}
	for _, role := range rule.ActorRoles {
		add("actor.role", role)
	}
	for _, role := range rule.GroupRoles {
		add("actor.group_role", string(security.ParseGroupRole(role)))
	}
	return out
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
		Type:           r.Action,
		Field:          r.Field,
		Text:           r.Text,
		Pattern:        r.Pattern,
		Replace:        r.Replace,
		Kind:           r.Kind,
		Path:           r.Path,
		Timing:         r.Timing,
		Tool:           r.Tool,
		Arguments:      r.Args,
		Command:        r.Command,
		Cwd:            r.Cwd,
		Stdin:          r.Stdin,
		Stdout:         r.Stdout,
		TimeoutSeconds: r.TimeoutSeconds,
		All:            r.All,
		Target:         r.Target,
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

	if err := rule.validateRoles(); err != nil {
		return fmt.Errorf("hook rule %q: %w", rule.Name, err)
	}

	for _, action := range rule.Actions {
		if strings.TrimSpace(action.Type) == "" {
			return fmt.Errorf("hook rule %q has action without type", rule.Name)
		}
		if err := validateActionTiming(action.Timing); err != nil {
			return fmt.Errorf("hook rule %q action %q: %w", rule.Name, firstNonEmpty(action.Name, action.Type), err)
		}
		if strings.TrimSpace(action.Type) == "exec" {
			if err := validateExecAction(action); err != nil {
				return fmt.Errorf("hook rule %q action %q: %w", rule.Name, firstNonEmpty(action.Name, action.Type), err)
			}
		}
	}
	return nil
}

func (r Rule) validateRoles() error {
	for _, role := range r.Roles {
		role = strings.TrimSpace(role)
		switch security.Role(role) {
		case security.RoleSuperadmin, security.RoleUser:
			continue
		}
		if security.ParseGroupRole(role) == security.GroupRoleUnknown && role != string(security.GroupRoleUnknown) {
			return fmt.Errorf("unsupported role %q", role)
		}
	}
	for _, role := range r.ActorRoles {
		switch security.Role(strings.TrimSpace(role)) {
		case security.RoleSuperadmin, security.RoleUser:
		default:
			return fmt.Errorf("unsupported actor role %q", role)
		}
	}
	for _, role := range r.GroupRoles {
		if security.ParseGroupRole(role) == security.GroupRoleUnknown && strings.TrimSpace(role) != string(security.GroupRoleUnknown) {
			return fmt.Errorf("unsupported group role %q", role)
		}
	}
	return nil
}

func validateActionTiming(timing string) error {
	return delivery.ValidateDeliveryTiming(timing)
}

func (m Module) runRule(ctx context.Context, rule Rule, event hook.Event) (hook.Event, error) {
	if !rule.matchRoles(event) {
		return event, nil
	}
	if rule.Consume {
		event.Control.Consume = true
	}
	if rule.StopPropagation {
		event.Control.StopPropagation = true
	}
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

func (r Rule) matchRoles(event hook.Event) bool {
	if len(r.Roles) > 0 && !matchesUnifiedRole(r.Roles, event) {
		return false
	}
	if len(r.ActorRoles) > 0 && !containsString(r.ActorRoles, event.Actor.Role) {
		return false
	}
	if len(r.GroupRoles) > 0 && !containsString(r.GroupRoles, event.Actor.GroupRole) {
		return false
	}
	return true
}

func matchesUnifiedRole(roles []string, event hook.Event) bool {
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == event.Actor.Role || role == event.Actor.GroupRole {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
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
		outputs, err := makeOutputs(action, event, state)
		if err != nil {
			return event, err
		}
		event.Outputs = append(event.Outputs, outputs...)
		return event, nil
	case "exec":
		updated, result, err := m.runExec(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.Name), "exec")
		if key != "" {
			state.Actions[key] = result
		}
		return updated, err
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

func makeOutputs(action Action, event hook.Event, state state) ([]delivery.Output, error) {
	target := delivery.Target{
		Platform:      render(action.Target.Platform, event, state),
		ScopeID:       render(action.Target.ScopeID, event, state),
		PrivateUserID: render(action.Target.PrivateUserID, event, state),
		GroupID:       render(action.Target.GroupID, event, state),
		Superadmins:   action.Target.Superadmins,
	}
	if strings.TrimSpace(target.Platform) == "" && event.Point == hook.PointPlatformConnected {
		target.Platform = event.Platform.Name
	}
	timing := render(action.Timing, event, state)

	if len(action.Segments) > 0 {
		outputs := make([]delivery.Output, 0, len(action.Segments))
		for _, seg := range action.Segments {
			seg := SegmentSpec{
				Kind:     render(seg.Kind, event, state),
				Text:     render(seg.Text, event, state),
				URL:      render(seg.URL, event, state),
				Path:     render(seg.Path, event, state),
				Base64:   render(seg.Base64, event, state),
				Name:     render(seg.Name, event, state),
				MIMEType: render(seg.MIMEType, event, state),
			}
			out, err := buildSegmentOutput(seg, target, timing)
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, out)
		}
		return outputs, nil
	}

	// Fallback: single output from kind/text/path.
	kind := delivery.Kind(strings.TrimSpace(action.Kind))
	if kind == "" {
		kind = delivery.KindText
	}
	text := render(action.Text, event, state)
	path := render(action.Path, event, state)
	var out delivery.Output
	switch kind {
	case delivery.KindText:
		out = delivery.Text(text)
	case delivery.KindImage:
		out = delivery.ImagePath(path)
		out.Text = text
	case delivery.KindFile:
		out = delivery.FilePath(path)
		out.Text = text
	case delivery.KindEmoticon:
		out = delivery.Emoticon(text)
		out.Source.Path = path
	case delivery.KindAt:
		out = delivery.At(text)
	default:
		return nil, fmt.Errorf("unsupported output kind %q", kind)
	}
	out.Target = target
	out = delivery.WithDeliveryTiming(out, timing)
	return []delivery.Output{out}, nil
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
	if !policy.CanUseTool(actor, assessment.Level, t.Info().OwnerScoped) {
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
		"{{platform.message_id}}":          event.Platform.PlatformMessageID,
		"{{platform.reply_to_message_id}}": event.Platform.ReplyToMessageID,
		"{{actor.id}}":                     event.Actor.ID,
		"{{actor.user_id}}":                event.Actor.UserID,
		"{{actor.role}}":                   event.Actor.Role,
		"{{actor.group_role}}":             event.Actor.GroupRole,
		"{{message.text}}":                 llm.SegmentsTextOnly(event.Message.Segments),
		"{{message.content_text}}":         llm.SegmentsContentText(event.Message.Segments),
		"{{llm.text}}":                     event.LLM.Text,
		"{{llm.raw_text}}":                 event.LLM.RawText,
		"{{llm.latest_user_text}}":         llm.LatestUserSegmentTextOnly(event.LLM.Messages),
		"{{llm.latest_user_content_text}}": llm.LatestUserSegmentContentText(event.LLM.Messages),
		"{{tool.arguments}}":               event.Tool.Arguments,
		"{{tool.result}}":                  event.Tool.Result,
	}
	for index, match := range eventMatchContext(event).Regex {
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

func eventMatchContext(event hook.Event) hook.MatchContext {
	match, _ := event.Metadata["match"].(hook.MatchContext)
	return match
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// buildSegmentOutput converts a SegmentSpec to a delivery.Output with the given target and timing.
func buildSegmentOutput(spec SegmentSpec, target delivery.Target, timing string) (delivery.Output, error) {
	spec.Kind = strings.TrimSpace(spec.Kind)
	if spec.Kind == "" {
		spec.Kind = string(delivery.KindText)
	}
	var out delivery.Output
	switch spec.Kind {
	case string(delivery.KindText):
		out = delivery.Text(strings.TrimSpace(spec.Text))
	case string(delivery.KindImage):
		out = delivery.ImagePath(strings.TrimSpace(spec.Path))
		out.Text = strings.TrimSpace(spec.Text)
		out.Name = strings.TrimSpace(spec.Name)
		out.Source.MIMEType = strings.TrimSpace(spec.MIMEType)
		if u := strings.TrimSpace(spec.URL); u != "" {
			out.Source.URL = u
		}
		if b := strings.TrimSpace(spec.Base64); b != "" {
			data, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return delivery.Output{}, fmt.Errorf("decode base64: %w", err)
			}
			out.Source.Data = data
		}
	case string(delivery.KindFile):
		out = delivery.FilePath(strings.TrimSpace(spec.Path))
		out.Text = strings.TrimSpace(spec.Text)
		out.Name = strings.TrimSpace(spec.Name)
		out.Source.MIMEType = strings.TrimSpace(spec.MIMEType)
		if u := strings.TrimSpace(spec.URL); u != "" {
			out.Source.URL = u
		}
		if b := strings.TrimSpace(spec.Base64); b != "" {
			data, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return delivery.Output{}, fmt.Errorf("decode base64: %w", err)
			}
			out.Source.Data = data
		}
	case string(delivery.KindEmoticon):
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = strings.TrimSpace(spec.Text)
		}
		path := strings.TrimSpace(spec.Path)
		if path != "" {
			out = delivery.EmoticonPath(name, path)
		} else {
			out = delivery.Emoticon(name)
		}
	default:
		return delivery.Output{}, fmt.Errorf("unsupported segment kind %q", spec.Kind)
	}
	out.Target = target
	out = delivery.WithDeliveryTiming(out, timing)
	return out, nil
}

// setTextField overwrites a text field with the given value, using allowField for permission checks.
func setTextField(event hook.Event, field, value string) (hook.Event, error) {
	field = strings.TrimSpace(field)
	if err := allowField(event, field); err != nil {
		return event, err
	}
	switch field {
	case "message.text":
		event.Message.Segments = llm.TextSegments(value)
	case "llm.latest_user_text":
		event.LLM.Messages = llm.SetLatestUserSegments(event.LLM.Messages, llm.TextSegments(value))
	case "llm.text":
		event.LLM.Text = value
	case "tool.arguments":
		event.Tool.Arguments = value
	case "tool.result":
		event.Tool.Result = value
	default:
		return event, fmt.Errorf("unsupported set field %q", field)
	}
	return event, nil
}
