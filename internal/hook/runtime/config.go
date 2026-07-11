// Package runtime runs stateful hook.v2 processes. It deliberately lives under
// the Hook subsystem: a persistent hook is still a Hook, not another plugin
// dispatch path.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/tool"
)

type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusRunning  Status = "running"
	StatusDegraded Status = "degraded"
	StatusStopping Status = "stopping"
	StatusStopped  Status = "stopped"
	StatusFailed   Status = "failed"
)

type RestartConfig struct {
	Strategy            string `toml:"strategy"`
	InitialDelaySeconds int    `toml:"initial_delay_seconds"`
	MaxDelaySeconds     int    `toml:"max_delay_seconds"`
}

type ToolsConfig struct {
	Allow           []string `toml:"allow"`
	BackgroundAllow []string `toml:"background_allow"`
}

// Config is decoded from [plugin.runtime]. All stateful lifecycle values are
// explicit so a configuration never inherits surprising restart behaviour.
type Config struct {
	Stateful               bool          `toml:"stateful"`
	Command                string        `toml:"command"`
	Cwd                    string        `toml:"cwd"`
	StartupTimeoutSeconds  int           `toml:"startup_timeout_seconds"`
	ShutdownTimeoutSeconds int           `toml:"shutdown_timeout_seconds"`
	EventTimeoutSeconds    int           `toml:"event_timeout_seconds"`
	MaxWaitSeconds         int           `toml:"max_wait_seconds"`
	Restart                RestartConfig `toml:"restart"`
	Tools                  ToolsConfig   `toml:"tools"`

	ID          string           `toml:"-"`
	Description string           `toml:"-"`
	Dir         string           `toml:"-"`
	ConfigPath  string           `toml:"-"`
	Block       hook.BlockPolicy `toml:"-"`
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("hook id is required")
	}
	if !validID(c.ID) {
		return fmt.Errorf("hook id %q must contain only lowercase letters, digits, '-' or '_'", c.ID)
	}
	if !c.Stateful {
		return fmt.Errorf("[plugin.runtime] requires stateful = true")
	}
	if strings.TrimSpace(c.Command) == "" {
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

type Info struct {
	ID          string
	Description string
	Status      Status
	Detail      string
}

type Options struct {
	Registry  *tool.Registry
	Logger    *slog.Logger
	Audit     func(event string, attrs ...any)
	Send      func(context.Context, delivery.Target, delivery.Output) (delivery.Receipt, error)
	SharedDir string
}

func splitCommand(command string) ([]string, error) {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return nil, fmt.Errorf("runtime command is required")
	}
	return fields, nil
}

func resolveCwd(dir, cwd string) (string, error) {
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("runtime cwd must be relative to plugin directory")
	}
	path := filepath.Clean(filepath.Join(dir, cwd))
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime cwd escapes plugin directory")
	}
	return path, nil
}
