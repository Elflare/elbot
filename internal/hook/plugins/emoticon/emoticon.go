package emoticon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/hook"
	"elbot/internal/output"
)

const (
	ConfigFile      = "emoticon.toml"
	DefaultPriority = 1000
)

var tokenPattern = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

type Options struct {
	ConfigDir string
	Logger    *slog.Logger
}

type Config struct {
	Enabled  *bool  `toml:"enabled"`
	Priority int    `toml:"priority"`
	RootDir  string `toml:"root_dir"`
	Timing   string `toml:"timing"`
}

// Module extracts LLM emoticon tokens into separate output intents.
type Module struct {
	Config Config
	Logger *slog.Logger
}

func NewModule(opts Options) (Module, error) {
	cfg, path, err := loadConfig(opts.ConfigDir)
	if err != nil {
		return Module{}, err
	}
	module := Module{Config: cfg, Logger: opts.Logger}
	if module.Logger != nil {
		module.Logger.Info("hook plugin loaded", "plugin", "emoticon", "path", path, "enabled", cfg.enabled(), "priority", cfg.priority())
	}
	return module, nil
}

func (m Module) RegisterHooks(registrar hook.Registrar) error {
	if !m.Config.enabled() {
		return nil
	}
	if err := registrar.Register(hook.Registration{
		Point:    hook.PointLLMResponseReceived,
		Priority: m.Config.priority(),
		Name:     "plugins.emoticon",
		Match:    hook.Regex("llm.text", `\[\[[^\[\]]+\]\]`),
		Handler:  hook.HandlerFunc(m.rewriteLLMEmoticons),
	}); err != nil {
		return err
	}
	if m.Logger != nil {
		m.Logger.Info("hook plugin registered", "plugin", "emoticon", "point", hook.PointLLMResponseReceived, "priority", m.Config.priority())
	}
	return nil
}

func (m Module) rewriteLLMEmoticons(_ context.Context, event hook.Event) (hook.Event, error) {
	if event.Point != hook.PointLLMResponseReceived {
		return event, nil
	}
	changed := false
	content := event.LLM.Text
	cleaned := tokenPattern.ReplaceAllStringFunc(content, func(token string) string {
		match := tokenPattern.FindStringSubmatch(token)
		if len(match) < 2 {
			return token
		}
		name := strings.TrimSpace(match[1])
		if name == "" || !emoticonDirExists(m.Config.RootDir, name) {
			return token
		}
		changed = true
		if path := pickImage(m.Config.RootDir, name); path != "" {
			event.Outputs = append(event.Outputs, output.WithDeliveryTiming(output.EmoticonPath(name, path), m.Config.timing()))
		} else {
			event.Outputs = append(event.Outputs, output.WithDeliveryTiming(output.Emoticon(name), m.Config.timing()))
		}
		return ""
	})
	if !changed {
		return event, nil
	}
	event.LLM.Text = strings.TrimSpace(cleaned)
	return event, nil
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
		return Config{}, path, fmt.Errorf("read emoticon config %q: %w", path, err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, path, fmt.Errorf("parse emoticon config %q: %w", path, err)
	}
	if err := validateTiming(cfg.Timing); err != nil {
		return Config{}, path, fmt.Errorf("parse emoticon config %q: %w", path, err)
	}
	return cfg, path, nil
}

func (c Config) enabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c Config) priority() int {
	if c.Priority == 0 {
		return DefaultPriority
	}
	return c.Priority
}

func (c Config) timing() string {
	timing := strings.TrimSpace(c.Timing)
	if timing == "" {
		return output.DeliveryImmediate
	}
	return timing
}

func validateTiming(timing string) error {
	switch strings.TrimSpace(timing) {
	case "", output.DeliveryImmediate, output.DeliveryAfterAssistant:
		return nil
	default:
		return fmt.Errorf("unsupported timing %q", timing)
	}
}

func emoticonDirExists(rootDir, name string) bool {
	if strings.TrimSpace(rootDir) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(rootDir, name))
	return err == nil && info.IsDir()
}

func pickImage(rootDir, name string) string {
	if strings.TrimSpace(rootDir) == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(rootDir, name))
	if err != nil {
		return ""
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isImage(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(rootDir, name, entry.Name()))
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[rand.Intn(len(paths))]
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}
