// 定义规则加载、控制与运行时共用的插件配置。
package share

import (
	"elbot/internal/processenv"
	"fmt"
	"strings"
)

type RuntimeMode string

const (
	RuntimeModeOnce       RuntimeMode = "once"
	RuntimeModePersistent RuntimeMode = "persistent"
	RuntimeModeTransient  RuntimeMode = "transient"
)

type RuntimeRestartConfig struct {
	Strategy            string `toml:"strategy"`              // 重启策略：never、on_failure 或 always。
	InitialDelaySeconds int    `toml:"initial_delay_seconds"` // 首次重启等待秒数。
	MaxDelaySeconds     int    `toml:"max_delay_seconds"`     // 重启退避的最大秒数。
}

type RuntimeToolsConfig struct {
	Allow           []string `toml:"allow"`            // 本插件允许调用的工具名称。
	BackgroundAllow []string `toml:"background_allow"` // Allow 中允许脱离当前事件调用的工具名称。
}

// RuntimeConfig is decoded from [plugin.runtime]. Omitted mode is equivalent to
// once; only persistent and transient modes create hook.v2 workers.
type RuntimeConfig struct {
	Mode                   RuntimeMode          `toml:"mode"`                     // 插件执行模式；空值按 once 处理。
	Command                []string             `toml:"command"`                  // 启动命令及参数。
	Cwd                    string               `toml:"cwd"`                      // 相对插件目录的工作路径。
	StartupTimeoutSeconds  int                  `toml:"startup_timeout_seconds"`  // 启动握手期限，单位秒。
	ShutdownTimeoutSeconds int                  `toml:"shutdown_timeout_seconds"` // 退出等待期限，单位秒。
	EventTimeoutSeconds    int                  `toml:"event_timeout_seconds"`    // 单次事件处理期限，单位秒。
	MaxWaitSeconds         int                  `toml:"max_wait_seconds"`         // 续接等待的最大秒数。
	Restart                RuntimeRestartConfig `toml:"restart"`                  // 插件重启策略与退避参数。
	Tools                  RuntimeToolsConfig   `toml:"tools"`                    // 前台和后台工具调用授权配置。

	ID          string                 `toml:"-"` // 装配后固定的插件标识。
	Description string                 `toml:"-"` // 插件展示说明。
	Dir         string                 `toml:"-"` // 插件目录的绝对路径。
	ConfigPath  string                 `toml:"-"` // 来源配置文件路径。
	Block       BlockPolicy            `toml:"-"` // 插件全部调用共用的阻断策略。
	ProcessEnv  processenv.Environment `toml:"-"` // 加载配置时固定的进程环境。
}

func (c RuntimeConfig) ModeOrOnce() RuntimeMode {
	if c.Mode == "" {
		return RuntimeModeOnce
	}
	return c.Mode
}

func (c RuntimeConfig) IsWorker() bool {
	return c.ModeOrOnce() != RuntimeModeOnce
}

func (c RuntimeConfig) Validate() error {
	mode := c.ModeOrOnce()
	if mode == RuntimeModeOnce {
		return nil
	}
	if mode != RuntimeModePersistent && mode != RuntimeModeTransient {
		return fmt.Errorf("runtime mode must be once, persistent or transient")
	}
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("hook id is required")
	}
	if !validID(c.ID) {
		return fmt.Errorf("hook id %q must contain only lowercase letters, digits, '-' or '_'", c.ID)
	}
	if len(c.Command) == 0 || strings.TrimSpace(c.Command[0]) == "" {
		return fmt.Errorf("runtime command is required")
	}
	if strings.TrimSpace(c.Cwd) == "" {
		return fmt.Errorf("runtime cwd is required")
	}
	if c.StartupTimeoutSeconds <= 0 || c.ShutdownTimeoutSeconds <= 0 || c.EventTimeoutSeconds <= 0 || c.MaxWaitSeconds <= 0 {
		return fmt.Errorf("runtime startup_timeout_seconds, shutdown_timeout_seconds, event_timeout_seconds and max_wait_seconds must be positive")
	}
	strategy := strings.TrimSpace(c.Restart.Strategy)
	if strategy != "never" && strategy != "on_failure" && strategy != "always" {
		return fmt.Errorf("runtime restart.strategy must be never, on_failure or always")
	}
	if c.Restart.InitialDelaySeconds <= 0 || c.Restart.MaxDelaySeconds <= 0 || c.Restart.InitialDelaySeconds > c.Restart.MaxDelaySeconds {
		return fmt.Errorf("runtime restart delays must be positive and initial_delay_seconds cannot exceed max_delay_seconds")
	}
	if strings.TrimSpace(c.Dir) == "" {
		return fmt.Errorf("runtime plugin directory is required")
	}
	seen := map[string]bool{}
	for _, name := range c.Tools.Allow {
		if name == "" || seen[name] {
			return fmt.Errorf("runtime tools.allow contains an empty or duplicate tool name")
		}
		seen[name] = true
	}
	for _, name := range c.Tools.BackgroundAllow {
		if !seen[name] {
			return fmt.Errorf("runtime tools.background_allow tool %q must also appear in tools.allow", name)
		}
	}
	for _, seconds := range []int{c.StartupTimeoutSeconds, c.ShutdownTimeoutSeconds, c.EventTimeoutSeconds, c.MaxWaitSeconds, c.Restart.InitialDelaySeconds, c.Restart.MaxDelaySeconds} {
		if int64(seconds) > int64((1<<63-1)/1_000_000_000) {
			return fmt.Errorf("runtime duration exceeds time.Duration limit")
		}
	}
	return nil
}

func validID(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return value != ""
}
