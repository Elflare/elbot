package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/tool"
)

const (
	ConfigFile      = "hooks.toml"
	DefaultPriority = 1000
)

type Options struct {
	ConfigDir       string
	Tools           *tool.Registry
	Logger          *slog.Logger
	Audit           func(event string, attrs ...any)
	Notify          func(context.Context, string)
	Send            func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
	PlatformCallers PlatformCallerResolver
	Runtime         *hookruntime.Manager
}

type Config struct {
	Plugins  []PluginRef          `toml:"plugins"`
	Rules    []Rule               `toml:"rules"`
	Runtimes []hookruntime.Config `toml:"-"`
}

type PluginRef struct {
	Name    string `toml:"name"`
	Enabled *bool  `toml:"enabled"`
	Path    string `toml:"path"`
}

type PluginInfo struct {
	Name             string              `toml:"name"`
	Description      string              `toml:"description"`
	BlockedPlatforms []string            `toml:"blocked_platform"`
	BlockedGroups    []string            `toml:"blocked_group"`
	BlockedIDs       []string            `toml:"blocked_id"`
	Runtime          *hookruntime.Config `toml:"runtime"`
}

type pluginConfig struct {
	Plugin PluginInfo `toml:"plugin"`
	Rules  []Rule     `toml:"rules"`
}

type Rule struct {
	Name             string            `toml:"name"`
	Description      string            `toml:"description"`
	On               string            `toml:"on"`
	Priority         int               `toml:"priority"`
	Enabled          *bool             `toml:"enabled"`
	Wakeup           hook.WakeupPolicy `toml:"wakeup"`
	BlockedPlatforms []string          `toml:"blocked_platform"`
	BlockedGroups    []string          `toml:"blocked_group"`
	BlockedIDs       []string          `toml:"blocked_id"`
	If               string            `toml:"if"`
	Op               string            `toml:"op"`
	Value            string            `toml:"value"`
	Always           bool              `toml:"always"`
	Match            []hook.Condition  `toml:"match"`
	Roles            []string          `toml:"roles"`
	ActorRoles       []string          `toml:"actor_roles"`
	GroupRoles       []string          `toml:"group_roles"`
	Action           string            `toml:"action"`
	Actions          []Action          `toml:"actions"`
	Field            string            `toml:"field"`
	Text             string            `toml:"text"`
	Pattern          string            `toml:"pattern"`
	Replace          string            `toml:"replace"`
	Kind             string            `toml:"kind"`
	Path             string            `toml:"path"`
	Timing           string            `toml:"timing"`
	Tool             string            `toml:"tool"`
	Args             string            `toml:"arguments"`
	Command          []string          `toml:"command"`
	Cwd              string            `toml:"cwd"`
	TimeoutSeconds   int               `toml:"timeout_seconds"`
	All              bool              `toml:"all"`
	Target           Target            `toml:"target"`
	Consume          bool              `toml:"consume"`
	StopPropagation  bool              `toml:"stop_propagation"`
	source           ruleSource
}

type Action struct {
	ActionName     string        `toml:"action_name"`
	Type           string        `toml:"type"`
	Field          string        `toml:"field"`
	Text           string        `toml:"text"`
	Pattern        string        `toml:"pattern"`
	Match          string        `toml:"-"`
	Replace        string        `toml:"replace"`
	Kind           string        `toml:"kind"`
	URL            string        `toml:"url"`
	Path           string        `toml:"path"`
	Base64         string        `toml:"base64"`
	Name           string        `toml:"name"`
	MIMEType       string        `toml:"mime_type"`
	UserID         string        `toml:"user_id"`
	MessageID      string        `toml:"message_id"`
	EmoticonID     string        `toml:"emoticon_id"`
	Timing         string        `toml:"timing"`
	Tool           string        `toml:"tool"`
	Arguments      string        `toml:"arguments"`
	All            bool          `toml:"all"`
	Command        []string      `toml:"command"`
	Cwd            string        `toml:"cwd"`
	TimeoutSeconds int           `toml:"timeout_seconds"`
	Target         Target        `toml:"target"`
	Outputs        []SegmentSpec `toml:"outputs"`
	source         ruleSource
}

type SegmentSpec = hookoutput.Segment
type Target = hookoutput.Target

type Module struct {
	Rules    []Rule
	Runtimes []hookruntime.Config
	Opts     Options
	Logger   *slog.Logger
}

type ruleSource struct {
	PluginName        string
	PluginDescription string
	ConfigPath        string
	BaseDir           string
	StrictDir         string
	OriginalName      string
	FinalName         string
	RuntimeID         string
	Block             hook.BlockPolicy
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
	module := Module{Rules: cfg.Rules, Runtimes: cfg.Runtimes, Opts: opts, Logger: opts.Logger}
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
				Point:       hook.Point(rule.On),
				Priority:    priority,
				PluginID:    rule.source.PluginName,
				Name:        regName,
				Description: description,
				Match:       match,
				Block:       rule.source.Block,
				Detail:      detail,
				Wakeup:      rule.Wakeup,
				Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
					if rule.source.RuntimeID != "" {
						if m.Opts.Runtime == nil {
							return event, fmt.Errorf("stateful hook runtime is not configured")
						}
						return m.Opts.Runtime.Handle(ctx, rule.source.RuntimeID, event, hook.Control{
							Consume:         rule.Consume,
							StopPropagation: rule.StopPropagation,
						})
					}
					return m.runRule(ctx, rule, event)
				}),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
