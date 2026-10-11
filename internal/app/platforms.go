package app

import (
	"context"

	platformmodule "elbot/internal/platform/module"
)

type defaultPlatformFactory struct{}

func (defaultPlatformFactory) Build(ctx context.Context, req PlatformRequest) (*platformmodule.Module, error) {
	m, err := platformmodule.NewModule(ctx, platformmodule.ModuleDeps{
		ConfigPath:         req.Foundation.Config.ConfigPath,
		Platform:           req.Foundation.Config.Platform,
		Superadmins:        req.Foundation.Config.Security.Superadmins,
		Prefixes:           req.Foundation.Config.Commands.Prefixes,
		SandboxRoot:        req.Foundation.Config.Sandbox.Root,
		Files:              req.Foundation.Config.PlatformFiles,
		Mode:               platformMode(req.Mode),
		Store:              req.Foundation.Store,
		ChatHistory:        req.Foundation.ChatHistory,
		Media:              req.Media,
		MediaRetentionDays: req.Foundation.Config.Maintenance.SandboxCleanup.RetentionDays,
	})
	req.Profiler.Mark("platform init")
	return m, err
}
