package builtin

import (
	"log/slog"

	"elbot/internal/config"
	"elbot/internal/platform"
	"elbot/internal/platform/cli"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/storage"
)

// Bundle contains platform adapters created from application config.
type Bundle struct {
	Primary  platform.PlatformAdapter
	Runtimes []platform.Runtime
}

func New(cfg *config.Config, store storage.Store, chatHistory storage.ChatHistoryRepository, logger *slog.Logger) (Bundle, error) {
	cliAdapter := cli.New()
	bundle := Bundle{Primary: cliAdapter, Runtimes: []platform.Runtime{cliAdapter}}
	if cfg == nil {
		return bundle, nil
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
	return bundle, nil
}
