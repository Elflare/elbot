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
	ConfigDir       string
	Tools           *tool.Registry
	Policy          *security.Policy
	Logger          *slog.Logger
	Audit           func(event string, attrs ...any)
	Notify          func(context.Context, string)
	Send            func(context.Context, delivery.Target, delivery.Output) (delivery.Receipt, error)
	PlatformCallers PlatformCallerResolver
}

type Config struct {
	Plugins []PluginRef `toml:"plugins"`
	Rules   []Rule      `toml:"rules"`
}

type PluginRef struct {
	Name    string `toml:"name"`
	Enabled *bool  `toml:"enabled"`
	Path    string `toml:"path"`
}

type PluginInfo struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
}

type pluginConfig struct {
	Plugin PluginInfo `toml:"plugin"`
	Rules  []Rule     `toml:"rules"`
}

type Rule struct {
	Name            string           `toml:"name"`
	Description     string           `toml:"description"`
	On              string           `toml:"on"`
	Priority        int              `toml:"priority"`
	Enabled         *bool            `toml:"enabled"`
	RequireWakeup   *bool            `toml:"require_wakeup"`
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
	TimeoutSeconds  int              `toml:"timeout_seconds"`
	All             bool             `toml:"all"`
	Target          Target           `toml:"target"`
	Consume         bool             `toml:"consume"`
	StopPropagation bool             `toml:"stop_propagation"`
	source          ruleSource
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
	TimeoutSeconds int           `toml:"timeout_seconds"`
	Target         Target        `toml:"target"`
	Outputs        []SegmentSpec `toml:"outputs"`
	source         ruleSource
}

type SegmentSpec struct {
	Kind      string `toml:"kind" json:"kind"`
	Text      string `toml:"text" json:"text,omitempty"`
	URL       string `toml:"url" json:"url,omitempty"`
	Path      string `toml:"path" json:"path,omitempty"`
	Base64    string `toml:"base64" json:"base64,omitempty"`
	Name      string `toml:"name" json:"name,omitempty"`
	MIMEType  string `toml:"mime_type" json:"mime_type,omitempty"`
	UserID    string `toml:"user_id" json:"user_id,omitempty"`
	MessageID string `toml:"message_id" json:"message_id,omitempty"`
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

type ruleSource struct {
	PluginName        string
	PluginDescription string
	ConfigPath        string
	BaseDir           string
	StrictDir         string
	OriginalName      string
	FinalName         string
}

type PlatformAPICaller interface {
	CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error)
}

type PlatformCallerResolver interface {
	PlatformCaller(platform string) (PlatformAPICaller, bool)
}

func NewModule(opts Options) (Module, error) {
	cfg, path, err := loadConfig(opts)
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
		name := strings.TrimSpace(rule.source.FinalName)
		if name == "" {
			name = strings.TrimSpace(rule.Name)
		}
		if name == "" {
			name = fmt.Sprintf("rule.%d", index+1)
		}
		if m.Logger != nil {
			m.Logger.Info("hook rule registered", "name", name, "point", rule.On, "priority", priority, "matches", len(rule.Match), "actions", len(rule.Actions), "config_path", rule.source.ConfigPath)
		}
		rule := rule
		registrations := ruleRegistrations(rule)
		for roleIndex, match := range registrations {
			regName := name
			if len(registrations) > 1 {
				regName = fmt.Sprintf("%s.role.%d", regName, roleIndex+1)
			}
			description := ruleDescription(rule)
			detail := formatRuleDetail(rule)
			if len(registrations) > 1 {
				detail = fmt.Sprintf("%s\n\n(role partition %d/%d)", detail, roleIndex+1, len(registrations))
			}
			if err := registrar.Register(hook.Registration{
				Point:         hook.Point(rule.On),
				Priority:      priority,
				Name:          regName,
				Description:   description,
				Match:         match,
				Detail:        detail,
				RequireWakeup: rule.RequireWakeup,
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
	return nil
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

func loadConfig(opts Options) (Config, string, error) {
	configDir := strings.TrimSpace(opts.ConfigDir)
	if configDir == "" {
		return Config{}, "", nil
	}
	path := filepath.Join(configDir, ConfigFile)
	var cfg Config
	if err := decodeTOMLFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, path, nil
		}
		return Config{}, path, err
	}

	rootSource := ruleSource{ConfigPath: path, BaseDir: configDir}
	for i := range cfg.Rules {
		cfg.Rules[i].source = rootSource
	}

	for _, ref := range cfg.Plugins {
		if !ref.enabled() {
			continue
		}
		pluginPath, err := pluginConfigPath(configDir, ref)
		if err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), "", err)
			continue
		}
		var pcfg pluginConfig
		if err := decodeTOMLFile(pluginPath, &pcfg); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		if infoName := strings.TrimSpace(pcfg.Plugin.Name); infoName != "" && infoName != strings.TrimSpace(ref.Name) {
			reportPluginConfigWarning(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, fmt.Sprintf("[plugin].name %q differs from [[plugins]].name %q; using the referenced plugin name", infoName, strings.TrimSpace(ref.Name)))
		}
		pluginDir := filepath.Dir(pluginPath)
		source := ruleSource{
			PluginName:        strings.TrimSpace(ref.Name),
			PluginDescription: strings.TrimSpace(pcfg.Plugin.Description),
			ConfigPath:        pluginPath,
			BaseDir:           pluginDir,
			StrictDir:         pluginDir,
		}
		for i := range pcfg.Rules {
			pcfg.Rules[i].source = source
		}
		pluginRules := Config{Rules: append([]Rule(nil), pcfg.Rules...)}
		if err := normalizeLoadedRules(&pluginRules, opts); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		if err := validateLoadedRules(pluginRules.Rules); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		cfg.Rules = append(cfg.Rules, pcfg.Rules...)
	}

	if err := normalizeLoadedRules(&cfg, opts); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	return cfg, path, nil
}

func decodeTOMLFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hook rule config %q: %w", path, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return detailedTOMLDecodeError(path, string(data), err)
	}
	return nil
}

func detailedTOMLDecodeError(path, data string, err error) error {
	var strictErr *toml.StrictMissingError
	if errors.As(err, &strictErr) && len(strictErr.Errors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		for _, decodeErr := range strictErr.Errors {
			appendDecodeErrorDetail(&b, data, decodeErr)
		}
		return configParseError{message: b.String(), cause: err}
	}

	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		appendDecodeErrorDetail(&b, data, *decodeErr)
		return configParseError{message: b.String(), cause: err}
	}

	if detailed, ok := detailedGenericTOMLParseError(path, data, err); ok {
		return detailed
	}

	return fmt.Errorf("parse hook rule config %q: %w", path, err)
}

func detailedGenericTOMLParseError(path, data string, err error) (error, bool) {
	if !strings.Contains(err.Error(), "already exists as a") {
		return nil, false
	}
	key := conflictingTOMLKey(err.Error())
	if key == "" {
		return nil, false
	}
	row, column, previousRow, context := findTOMLArrayTableConflict(data, key)
	if row == 0 {
		return nil, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "parse hook rule config %q\n- line %d, column %d, field %q", path, row, column, key)
	if context != "" {
		fmt.Fprintf(&b, ", %s", context)
	}
	fmt.Fprintf(&b, ": %s", err.Error())
	if previousRow > 0 {
		fmt.Fprintf(&b, "；%q was already set on line %d", key, previousRow)
	}
	if snippet := sourceLineSnippet(data, row, column); snippet != "" {
		b.WriteString("\n")
		b.WriteString(indentLines(snippet, "  "))
	}
	return configParseError{message: b.String(), cause: err}, true
}

func conflictingTOMLKey(message string) string {
	match := regexp.MustCompile(`already exists as a\s+([A-Za-z0-9_-]+)`).FindStringSubmatch(message)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func findTOMLArrayTableConflict(data, key string) (row, column, previousRow int, context string) {
	lines := strings.Split(data, "\n")
	ruleName := ""
	seenKeyLine := 0
	seenArrayTableLine := 0
	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "[[rules]]" {
			ruleName = ""
			seenKeyLine = 0
			seenArrayTableLine = 0
			continue
		}
		if k, value, ok := parseTomlStringAssignment(trimmed); ok && k == "name" {
			ruleName = value
		}
		if parseTomlAssignmentKey(trimmed) == key {
			if seenArrayTableLine > 0 {
				return lineNo, lineColumn(line, key), seenArrayTableLine, ruleContext(ruleName)
			}
			seenKeyLine = lineNo
			continue
		}
		if isTomlArrayTableForKey(trimmed, key) {
			if seenKeyLine > 0 {
				return lineNo, lineColumn(line, "[["), seenKeyLine, ruleContext(ruleName)
			}
			seenArrayTableLine = lineNo
		}
	}
	return 0, 0, 0, ""
}

func parseTomlAssignmentKey(line string) string {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "[") {
		return ""
	}
	return key
}

func isTomlArrayTableForKey(line, key string) bool {
	return line == "[["+key+"]]" || strings.HasSuffix(line, "."+key+"]] ") || strings.HasSuffix(line, "."+key+"]] ") || strings.HasSuffix(line, "."+key+"]]")
}

func ruleContext(name string) string {
	if strings.TrimSpace(name) == "" {
		return "rule"
	}
	return fmt.Sprintf("rule %q", name)
}

func lineColumn(line, needle string) int {
	if index := strings.Index(line, needle); index >= 0 {
		return index + 1
	}
	return 1
}

func sourceLineSnippet(data string, row, column int) string {
	lines := strings.Split(data, "\n")
	if row <= 0 || row > len(lines) {
		return ""
	}
	line := lines[row-1]
	if column <= 0 {
		column = 1
	}
	return fmt.Sprintf("%d| %s\n | %s^", row, line, strings.Repeat(" ", column-1))
}

func appendDecodeErrorDetail(b *strings.Builder, data string, err toml.DecodeError) {
	row, column := err.Position()
	key := strings.Join([]string(err.Key()), ".")
	context := tomlContextAtLine(data, row)
	fmt.Fprintf(b, "\n- line %d, column %d", row, column)
	if key != "" {
		fmt.Fprintf(b, ", field %q", key)
	}
	if context != "" {
		fmt.Fprintf(b, ", %s", context)
	}
	fmt.Fprintf(b, ": %s", err.Error())
	if snippet := strings.TrimRight(err.String(), "\n"); snippet != "" {
		b.WriteString("\n")
		b.WriteString(indentLines(snippet, "  "))
	}
}

func tomlContextAtLine(data string, row int) string {
	if row <= 0 {
		return ""
	}
	lines := strings.Split(data, "\n")
	if row > len(lines) {
		row = len(lines)
	}
	section := ""
	ruleName := ""
	pluginName := ""
	for i := 0; i < row; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case line == "[[rules]]":
			section = "rule"
			ruleName = ""
		case line == "[[plugins]]":
			section = "plugin_ref"
			pluginName = ""
		case line == "[plugin]":
			section = "plugin_info"
		case strings.HasPrefix(line, "[rules.") || strings.HasPrefix(line, "[[rules."):
			section = "rule"
		case strings.HasPrefix(line, "["):
			section = line
		}
		key, value, ok := parseTomlStringAssignment(line)
		if !ok || key != "name" {
			continue
		}
		switch section {
		case "rule":
			ruleName = value
		case "plugin_ref":
			pluginName = value
		}
	}
	switch section {
	case "rule":
		if ruleName != "" {
			return fmt.Sprintf("rule %q", ruleName)
		}
		return "rule"
	case "plugin_ref":
		if pluginName != "" {
			return fmt.Sprintf("plugin ref %q", pluginName)
		}
		return "plugin ref"
	case "plugin_info":
		return "plugin metadata"
	default:
		if strings.HasPrefix(section, "[") {
			return "section " + section
		}
		return ""
	}
}

func parseTomlStringAssignment(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return key, "", false
	}
	return key, strings.Trim(value, "\""), true
}

func indentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

type configParseError struct {
	message string
	cause   error
}

func (e configParseError) Error() string { return e.message }

func (e configParseError) Unwrap() error { return e.cause }

func (p PluginRef) enabled() bool {
	return p.Enabled == nil || *p.Enabled
}

func pluginConfigPath(configDir string, ref PluginRef) (string, error) {
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return "", fmt.Errorf("plugin name is required")
	}
	rel := strings.TrimSpace(ref.Path)
	if rel == "" {
		rel = filepath.Join(name, "hook.toml")
	}
	return safeRelativePath(configDir, rel)
}

func safeRelativePath(base, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("path %q escapes plugins directory", rel)
	}
	joined := filepath.Join(base, clean)
	if !pathWithin(base, joined) {
		return "", fmt.Errorf("path %q escapes plugins directory", rel)
	}
	return joined, nil
}

func pathWithin(base, path string) bool {
	base = filepath.Clean(base)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func normalizeLoadedRules(cfg *Config, opts Options) error {
	seen := map[string]int{}
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		if err := rule.normalize(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		original := strings.TrimSpace(rule.Name)
		if original == "" {
			original = fmt.Sprintf("rule.%d", i+1)
			rule.Name = original
		}
		rule.source.OriginalName = original
		final := original
		if count := seen[original]; count > 0 {
			final = fmt.Sprintf("%s.%d", original, count)
			if opts.Logger != nil {
				opts.Logger.Warn("duplicate hook rule name renamed", "name", original, "final_name", final, "config_path", rule.source.ConfigPath)
			}
		}
		seen[original]++
		rule.source.FinalName = final
		for j := range rule.Actions {
			rule.Actions[j].source = rule.source
		}
	}
	return nil
}

func validateLoadedRules(rules []Rule) error {
	for i, rule := range rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func reportPluginConfigError(ctx context.Context, opts Options, name, path string, err error) {
	if opts.Logger != nil {
		opts.Logger.Warn("hook plugin skipped", "plugin", name, "path", path, "error", err)
	}
	if opts.Notify != nil {
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		opts.Notify(ctx, fmt.Sprintf("Hook 插件 %s 已跳过：%v", label, err))
	}
}

func reportPluginConfigWarning(ctx context.Context, opts Options, name, path, message string) {
	if opts.Logger != nil {
		opts.Logger.Warn("hook plugin warning", "plugin", name, "path", path, "warning", message)
	}
	if opts.Notify != nil {
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		opts.Notify(ctx, fmt.Sprintf("Hook 插件 %s 警告：%s", label, message))
	}
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
	return normalizeLoadedRules(c, Options{})
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
	before := event
	state := state{Actions: map[string]actionResult{}}
	var err error
	for index, action := range rule.Actions {
		action.source = rule.source
		var result actionResult
		event, result, err = m.runAction(ctx, event, action, state)
		if err != nil {
			return event, fmt.Errorf("rule %q action %d %s: %w", rule.Name, index+1, action.Type, err)
		}
		if result.Matched != nil && !*result.Matched {
			return before, nil
		}
	}
	if rule.Consume {
		event.Control.Consume = true
	}
	if rule.StopPropagation {
		event.Control.StopPropagation = true
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
	Result  string
	Error   string
	Matched *bool
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
		key := firstNonEmpty(strings.TrimSpace(action.Name), "exec")
		if key != "" {
			state.Actions[key] = result
		}
		return updated, result, err
	case "tool":
		result, err := m.callTool(ctx, event, action, state)
		key := firstNonEmpty(strings.TrimSpace(action.Name), strings.TrimSpace(action.Tool))
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

	if len(action.Outputs) > 0 {
		outputs := make([]delivery.Output, 0, len(action.Outputs))
		for _, seg := range action.Outputs {
			seg := SegmentSpec{
				Kind:      render(seg.Kind, event, state),
				Text:      render(seg.Text, event, state),
				URL:       render(seg.URL, event, state),
				Path:      render(seg.Path, event, state),
				Base64:    render(seg.Base64, event, state),
				Name:      render(seg.Name, event, state),
				MIMEType:  render(seg.MIMEType, event, state),
				UserID:    render(seg.UserID, event, state),
				MessageID: render(seg.MessageID, event, state),
			}
			seg = resolveSegmentSpecPath(seg, action.sourceBaseDir())
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
	path := resolveLocalPath(render(action.Path, event, state), action.sourceBaseDir())
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
	case delivery.KindReply:
		out = delivery.Reply(path, text)
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
	replacements := hook.TemplateValues(event)
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
	case string(delivery.KindAt):
		userID := strings.TrimSpace(spec.UserID)
		if userID == "" {
			userID = strings.TrimSpace(spec.Text)
		}
		out = delivery.At(userID)
	case string(delivery.KindReply):
		messageID := strings.TrimSpace(spec.MessageID)
		if messageID == "" {
			messageID = strings.TrimSpace(spec.Path)
		}
		out = delivery.Reply(messageID, strings.TrimSpace(spec.Text))
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

func formatRuleDetail(rule Rule) string {
	var sb strings.Builder
	sb.WriteString("on: " + strings.TrimSpace(rule.On))

	if rule.Always {
		sb.WriteString("\nmatch: always")
	} else if len(rule.Match) > 0 {
		sb.WriteString("\nmatch:")
		for _, cond := range rule.Match {
			sb.WriteString("\n  " + cond.Field + " " + cond.Op + " " + strconvQuote(cond.Value))
		}
	} else if strings.TrimSpace(rule.If) != "" {
		sb.WriteString("\nmatch: " + rule.If + " " + rule.Op + " " + strconvQuote(rule.Value))
	}

	if len(rule.Roles) > 0 {
		sb.WriteString("\nroles: " + strings.Join(rule.Roles, ", "))
	}
	if len(rule.ActorRoles) > 0 {
		sb.WriteString("\nactor_roles: " + strings.Join(rule.ActorRoles, ", "))
	}
	if len(rule.GroupRoles) > 0 {
		sb.WriteString("\ngroup_roles: " + strings.Join(rule.GroupRoles, ", "))
	}

	for i, action := range rule.Actions {
		sb.WriteString(fmt.Sprintf("\naction[%d]: %s", i+1, action.Type))
		if action.Name != "" {
			sb.WriteString(" (" + action.Name + ")")
		}
		if action.Field != "" {
			sb.WriteString(" field=" + action.Field)
		}
		if action.Text != "" {
			sb.WriteString(" text=" + strconvQuote(action.Text))
		}
		if action.Match != "" {
			sb.WriteString(" pattern=" + strconvQuote(action.Match))
		}
		if action.Replace != "" {
			sb.WriteString(" replace=" + strconvQuote(action.Replace))
		}
		if action.Tool != "" {
			sb.WriteString(" tool=" + action.Tool)
		}
		if action.Arguments != "" {
			sb.WriteString(" args=" + strconvQuote(action.Arguments))
		}
		if action.Command != "" {
			sb.WriteString(" command=" + strconvQuote(action.Command))
		}
		if action.Timing != "" {
			sb.WriteString(" timing=" + action.Timing)
		}
		if action.Target.Platform != "" || action.Target.ScopeID != "" || action.Target.PrivateUserID != "" || action.Target.GroupID != "" {
			sb.WriteString(" target=" + targetString(action.Target))
		}
		if action.All {
			sb.WriteString(" all=true")
		}
		if len(action.Outputs) > 0 {
			sb.WriteString(fmt.Sprintf(" outputs=%d", len(action.Outputs)))
		}
	}

	if rule.Consume {
		sb.WriteString("\nconsume: true")
	}
	if rule.StopPropagation {
		sb.WriteString("\nstop_propagation: true")
	}
	if rule.RequireWakeup != nil && !*rule.RequireWakeup {
		sb.WriteString("\nrequire_wakeup: false")
	}
	if rule.Priority != 0 {
		sb.WriteString(fmt.Sprintf("\npriority: %d", rule.Priority))
	}

	return sb.String()
}

func ruleDescription(rule Rule) string {
	if description := strings.TrimSpace(rule.Description); description != "" {
		return description
	}
	return strings.TrimSpace(rule.source.PluginDescription)
}

func targetString(t Target) string {
	parts := []string{}
	if t.Platform != "" {
		parts = append(parts, "platform="+t.Platform)
	}
	if t.ScopeID != "" {
		parts = append(parts, "scope_id="+t.ScopeID)
	}
	if t.PrivateUserID != "" {
		parts = append(parts, "private_user_id="+t.PrivateUserID)
	}
	if t.GroupID != "" {
		parts = append(parts, "group_id="+t.GroupID)
	}
	if t.Superadmins {
		parts = append(parts, "superadmins=true")
	}
	return strings.Join(parts, ",")
}

func strconvQuote(s string) string {
	if s == "" {
		return "\"\""
	}
	return fmt.Sprintf("%q", s)
}
