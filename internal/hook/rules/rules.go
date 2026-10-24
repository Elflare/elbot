// 定义规则、动作和消费依赖，并构造和注册规则集。

package rules

import (
	"context"
	"elbot/internal/processenv"
	"encoding/json"
	"fmt"
	"log/slog"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	hookoutput "elbot/internal/hook/output"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/hook/share"
	"elbot/internal/tool"
)

const (
	ConfigFile      = "hooks.toml"
	DefaultPriority = 1000
)

type Options struct {
	ConfigDir string
	Tools     interface {
		ToolInfo(string) (tool.Info, bool)
		CallTool(context.Context, tool.CallRequest) (*tool.Result, string, error)
	} // Borrowed metadata and call operations; no Tool objects escape their owner.

	Notices *[]string // Optional reload report collector; never a sending dependency.
	// Send returns business receipts, the delivery log outcome and the original error.
	Send            func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)
	CallPlatformAPI func(context.Context, string, string, map[string]any) (json.RawMessage, error)
	// Runtime borrows execution capabilities without exposing owned components.
	Runtime interface {
		Handle(context.Context, string, share.Event, share.Control) (share.Event, globalevents.LogResult, error)
		MediaRequest(context.Context, string, string, json.RawMessage) (any, error)
		SharedRequest(string, json.RawMessage) (any, error)
	}
	ProcessEnv processenv.Environment
	Log        func(context.Context, globalevents.LogRecord)          // Publishes a complete source-owned record.
	Notify     func(context.Context, globalevents.Notification) error // Publishes the notice chosen by this source.
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
	Name             string             `toml:"name"`
	Description      string             `toml:"description"`
	On               string             `toml:"on"`
	Priority         int                `toml:"priority"`
	Enabled          *bool              `toml:"enabled"`
	Wakeup           share.WakeupPolicy `toml:"wakeup"`
	BlockedPlatforms []string           `toml:"blocked_platform"`
	BlockedGroups    []string           `toml:"blocked_group"`
	BlockedIDs       []string           `toml:"blocked_id"`
	If               string             `toml:"if"`
	Op               string             `toml:"op"`
	Value            string             `toml:"value"`
	Always           bool               `toml:"always"`
	Match            []share.Condition  `toml:"match"`
	Roles            []string           `toml:"roles"`
	ActorRoles       []string           `toml:"actor_roles"`
	GroupRoles       []string           `toml:"group_roles"`
	Action           string             `toml:"action"`
	Actions          []Action           `toml:"actions"`
	Field            string             `toml:"field"`
	Text             string             `toml:"text"`
	Pattern          string             `toml:"pattern"`
	Replace          string             `toml:"replace"`
	Kind             string             `toml:"kind"`
	Path             string             `toml:"path"`
	Timing           string             `toml:"timing"`
	Tool             string             `toml:"tool"`
	Args             string             `toml:"arguments"`
	Command          []string           `toml:"command"`
	Cwd              string             `toml:"cwd"`
	TimeoutSeconds   int                `toml:"timeout_seconds"`
	All              bool               `toml:"all"`
	Target           Target             `toml:"target"`
	Consume          bool               `toml:"consume"`
	StopPropagation  bool               `toml:"stop_propagation"`
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
	Block             share.BlockPolicy
	ProcessEnv        processenv.Environment
}

func NewModule(opts Options) (Module, error) {
	cfg, path, err := loadConfig(opts)
	if err != nil {
		reportConfigError(context.Background(), opts, path, err)
		return Module{}, err
	}
	module := Module{Rules: cfg.Rules, Runtimes: cfg.Runtimes, Opts: opts}

	enabled := 0
	for _, rule := range module.Rules {
		if rule.enabled() {
			enabled++
		}
	}
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "hook_rule_config_loaded",
			Module:       "hook",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("%s\npath=%v\nrules=%v\nenabled=%v", "hook rule config loaded", path, len(module.Rules), enabled),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		opts.Log(context.Background(), record)
	}

	return module, nil
}

func (m Module) RegisterHooks(registrar share.Registrar) {
	for _, rule := range m.Rules {
		if !rule.enabled() {
			continue
		}
		priority := rule.Priority
		if priority == 0 {
			priority = DefaultPriority
		}
		name := rule.source.FinalName

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "hook_rule_registered",
				Module:       "hook",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("%s\nname=%v\npoint=%v\npriority=%v\nmatches=%v\nactions=%v\nconfig_path=%v", "hook rule registered", name, rule.On, priority, len(rule.Match), len(rule.Actions), rule.source.ConfigPath),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			m.Opts.Log(context.Background(), record)
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
			registrar.Register(share.Registration{
				Point:       share.Point(rule.On),
				Priority:    priority,
				PluginID:    rule.source.PluginName,
				Name:        regName,
				Description: description,
				Match:       match,
				Block:       rule.source.Block,
				Detail:      detail,
				Wakeup:      rule.Wakeup,
				Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, readOnlyCalls bool) (share.Event, globalevents.LogResult, error) {
					if rule.source.RuntimeID != "" {
						return m.Opts.Runtime.Handle(ctx, rule.source.RuntimeID, event, share.Control{
							Consume:         rule.Consume,
							StopPropagation: rule.StopPropagation,
						})
					}
					return m.runRule(ctx, rule, event, readOnlyCalls)
				}),
			})
		}
	}
}
