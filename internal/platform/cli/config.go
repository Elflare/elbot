package cli

import (
	"fmt"
	"strings"

	"elbot/internal/platform"
)

const defaultRemoteAddr = "127.0.0.1:32172"
const defaultRemotePath = "/cli/v1/ws"

type Config struct {
	Enabled       bool                    `toml:"enabled"`
	DefaultClient string                  `toml:"default_client"`
	DefaultURL    string                  `toml:"default_url"`
	Server        ServerConfig            `toml:"server"`
	Clients       map[string]ClientConfig `toml:"clients"`
}

type ServerConfig struct {
	Enabled bool                `toml:"enabled"`
	Listen  string              `toml:"listen"`
	Tokens  map[string][]string `toml:"tokens"`
}

type ClientConfig struct {
	ID       string   `toml:"id"`
	URL      string   `toml:"url"`
	TokenEnv []string `toml:"token_env"`
}

func NewConfigFromPlatformConfig(raw map[string]any) (Config, error) {
	cfg := defaultConfig()
	if err := platform.DecodeConfig(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode cli config: %w", err)
	}
	applyConfigDefaults(&cfg)
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Enabled:       true,
		DefaultClient: "local",
		DefaultURL:    "ws://" + defaultRemoteAddr + defaultRemotePath,
		Server: ServerConfig{
			Enabled: false,
			Listen:  defaultRemoteAddr,
			Tokens:  map[string][]string{},
		},
		Clients: map[string]ClientConfig{},
	}
}

func applyConfigDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.DefaultClient) == "" {
		cfg.DefaultClient = "local"
	}
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = defaultRemoteAddr
	}
	if strings.TrimSpace(cfg.DefaultURL) == "" {
		cfg.DefaultURL = "ws://" + cfg.Server.Listen + defaultRemotePath
	}
	if cfg.Server.Tokens == nil {
		cfg.Server.Tokens = map[string][]string{}
	}
	if cfg.Clients == nil {
		cfg.Clients = map[string]ClientConfig{}
	}
	for name, client := range cfg.Clients {
		if strings.TrimSpace(client.ID) == "" {
			client.ID = strings.TrimSpace(name)
		}
		if strings.TrimSpace(client.URL) == "" {
			client.URL = cfg.DefaultURL
		}
		cfg.Clients[name] = client
	}
}

func (cfg Config) Client(name string) (string, ClientConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(cfg.DefaultClient)
	}
	if name == "" && len(cfg.Clients) == 1 {
		for key := range cfg.Clients {
			name = key
		}
	}
	if name == "" {
		return "", ClientConfig{}, fmt.Errorf("cli client is not configured")
	}
	client, ok := cfg.Clients[name]
	if !ok {
		return "", ClientConfig{}, fmt.Errorf("cli client %q is not configured", name)
	}
	if strings.TrimSpace(client.ID) == "" {
		client.ID = name
	}
	if strings.TrimSpace(client.URL) == "" {
		client.URL = cfg.DefaultURL
	}
	return name, client, nil
}
