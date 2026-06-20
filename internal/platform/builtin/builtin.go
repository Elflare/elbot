package builtin

import (
	"log/slog"
	"path/filepath"

	"elbot/internal/config"
	"elbot/internal/platform"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/headless"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/telegram"
	"elbot/internal/storage"
)

type Mode string

const (
	ModeFull    Mode = "full"
	ModeCLIOnly Mode = "cli"
	ModeService Mode = "service"
)

type Options struct {
	Mode Mode
}

// Bundle contains platform adapters created from application config.
type Bundle struct {
	Primary  platform.PlatformAdapter
	Runtimes []platform.Runtime
}

func New(opts Options, cfg *config.Config, store storage.Store, chatHistory storage.ChatHistoryRepository, logger *slog.Logger) (Bundle, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeFull
	}

	var bundle Bundle
	if mode == ModeService {
		bundle.Primary = headless.New()
	} else {
		cliAdapter := cli.New()
		bundle.Primary = cliAdapter
		bundle.Runtimes = append(bundle.Runtimes, cliAdapter)
	}
	if cfg == nil || mode == ModeCLIOnly {
		return bundle, nil
	}
	if raw, ok := cfg.Platform["qqofficial"]; ok {
		attachmentDir := filepath.Join(cfg.Sandbox.Root, "platform", "qqofficial")
		adapter, err := qqofficial.NewFromPlatformConfig(raw, logger, cfg.Security.Superadmins["qqofficial"], attachmentDir)
		if err != nil {
			return Bundle{}, err
		}
		if adapter.Enabled() {
			bundle.Runtimes = append(bundle.Runtimes, adapter)
		}
	}
	if raw, ok := cfg.Platform["qqonebot"]; ok {
		adapter, err := qqonebot.NewFromPlatformConfig(raw, store, chatHistory, logger, cfg.Security.Superadmins["qqonebot"], cfg.Commands.Prefixes)
		if err != nil {
			return Bundle{}, err
		}
		if adapter.Enabled() {
			bundle.Runtimes = append(bundle.Runtimes, adapter)
		}
	}
	if raw, ok := cfg.Platform["telegram"]; ok {
		adapter, err := telegram.NewFromPlatformConfig(raw, store, chatHistory, logger, cfg.Security.Superadmins["telegram"], cfg.Commands.Prefixes, filepath.Dir(cfg.ConfigPath))
		if err != nil {
			return Bundle{}, err
		}
		if adapter.Enabled() {
			bundle.Runtimes = append(bundle.Runtimes, adapter)
		}
	}
	return bundle, nil
}
