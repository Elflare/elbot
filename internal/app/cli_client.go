package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"elbot/internal/config"
	"elbot/internal/platform/cli"
)

var ErrCLIClientFallback = errors.New("cli client fallback")

type CLIClientOptions struct {
	ConfigPath string
	ClientName string
}

func RunCLIClient(ctx context.Context, opts CLIClientOptions) error {
	client, err := newCLIClient(opts)
	if err != nil {
		return err
	}
	return client.Run(ctx)
}

func TryRunCLIClient(ctx context.Context, opts CLIClientOptions) error {
	client, err := newCLIClient(opts)
	if err != nil {
		if opts.ClientName == "" {
			return ErrCLIClientFallback
		}
		return err
	}
	err = client.Run(ctx)
	if err != nil && opts.ClientName == "" && isLocalCLIURL(client.URL()) {
		return ErrCLIClientFallback
	}
	return err
}

func newCLIClient(opts CLIClientOptions) (*cli.RemoteClient, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	raw := cfg.Platform["cli"]
	cliCfg, err := cli.NewConfigFromPlatformConfig(raw)
	if err != nil {
		return nil, err
	}
	if !cliCfg.Enabled {
		return nil, fmt.Errorf("cli platform is disabled")
	}
	return cli.NewRemoteClient(cli.RemoteClientOptions{Config: cliCfg, ConfigDir: filepath.Dir(cfg.ConfigPath), ClientName: opts.ClientName})
}

func isLocalCLIURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
