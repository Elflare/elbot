package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dialoguemodule "elbot/internal/agent/module"
	backgroundmodule "elbot/internal/background/module"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	hookmodule "elbot/internal/hook/module"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
	toolmodule "elbot/internal/tool/module"
	"elbot/internal/tool/runtimeinfo"
)

type defaultWiringFactory struct{}

func (defaultWiringFactory) Build(ctx context.Context, req WiringRequest) (*WiringComponents, error) {
	c := &WiringComponents{}
	lc := &wiringLifecycle{components: c, done: make(chan struct{})}
	c.Lifecycle = lc
	f, cfg := req.Foundation, req.Foundation.Config
	services, err := buildSharedServices(ctx, req)
	if err != nil {
		return c, err
	}
	lc.models = services.Models
	dotEnv, err := config.LoadDotEnv(filepath.Dir(cfg.ConfigPath))
	if err != nil {
		return c, fmt.Errorf("load process environment: %w", err)
	}
	baseEnv := processenv.New(os.Environ())
	c.Platform, err = req.Platforms.Build(ctx, PlatformRequest{Foundation: f, Mode: req.Mode, Media: services.Media, Profiler: req.Profiler})
	if err != nil {
		return c, err
	}
	info := runtimeinfo.Info{ConfigPath: cfg.ConfigPath, SandboxRoot: cfg.Sandbox.Root, FileDelivery: cfg.FileDelivery}
	c.Tool, err = toolmodule.NewModule(ctx, toolmodule.ModuleDeps{
		ConfigDir:            filepath.Dir(cfg.ConfigPath),
		RuntimeInfo:          info,
		Store:                f.Store,
		ChatHistory:          f.ChatHistory,
		Media:                services.Media,
		Policy:               services.Policy,
		ProcessEnv:           baseEnv.Fill(dotEnv),
		ResidentMemoryLimits: resident.Limits{Core: cfg.ResidentMemory.CoreMaxUnits, Normal: cfg.ResidentMemory.NormalMaxUnits},
		TagsPath:             cfg.ToolTagsConfigPath,
		Tags:                 cfg.ToolTags,
	})
	if err != nil {
		return c, err
	}
	req.Profiler.Mark("builtin tools register")
	send := func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return c.Platform.Dispatcher().SendTo(ctx, target, outputs)
	}
	c.Hook, err = hookmodule.NewModule(ctx, hookmodule.ModuleDeps{
		ConfigPath:      cfg.ConfigPath,
		Registry:        c.Tool.Registry(),
		Media:           services.Media,
		ProcessEnv:      hook.ProcessEnvironment(baseEnv),
		PlatformCallers: c.Platform.HookCallers(),
		Send:            send,
	})
	if err != nil {
		return c, err
	}
	req.Profiler.Mark("hook register")
	c.Dialogue, err = dialoguemodule.NewModule(ctx, dialoguemodule.ModuleDeps{
		Reasoning:       c.Platform.StartReasoning,
		PlatformName:    c.Platform.PrimaryName(),
		Store:           f.Store,
		Models:          services.Models,
		Policy:          services.Policy,
		Media:           services.Media,
		Dispatcher:      c.Platform.Dispatcher(),
		Doctor:          services.Doctor,
		Registry:        c.Tool.Registry(),
		ToolManager:     c.Tool.Manager(),
		Preloader:       c.Tool.Preloader(),
		ToolState:       c.Tool.State(),
		Files:           c.Tool.Files(),
		Skills:          c.Tool.Skills(),
		Memory:          c.Tool.Memory(),
		HookRunner:      c.Hook.Runner(),
		HookRouter:      c.Hook.Router(),
		HookControl:     c.Hook.Control(),
		Session:         cfg.Session,
		Context:         cfg.Context,
		Prefixes:        cfg.Commands.Prefixes,
		PageSize:        cfg.View.SessionListPageSize,
		RetentionDays:   cfg.Maintenance.SessionCleanup.RetentionDays,
		MaxRounds:       cfg.Tools.MaxRoundsPerTurn,
		ResponseTimeout: time.Duration(max(0, cfg.LLMRequest.ResponseTimeoutSeconds)) * time.Second,
		SoulPath:        cfg.Soul.Path,
		LogDir:          f.Logs.LogDir(),
	})
	if err != nil {
		return c, err
	}
	req.Profiler.Mark("agent init")
	c.Background, err = backgroundmodule.NewModule(ctx, backgroundmodule.ModuleDeps{
		ConfigPath:       cfg.ConfigPath,
		Maintenance:      cfg.Maintenance,
		Elnis:            cfg.Elnis,
		SandboxRoot:      cfg.Sandbox.Root,
		Platform:         cfg.Platform,
		Superadmins:      cfg.Security.Superadmins,
		EnabledPlatforms: c.Platform.Names(),
		PlatformCallers:  c.Platform,
		EnableWorkers:    shouldStartCron(req.Mode),
		Store:            f.Store,
		ChatHistory:      f.ChatHistory,
		Logs:             f.Logs,
		Media:            services.Media,
		Models:           services.Models,
		Sessions:         c.Dialogue.Sessions(),
		Identity:         c.Dialogue.Identity(),
		Execution:        c.Dialogue.Execution(),
		Registry:         c.Tool.Registry(),
		ToolInfo:         info,
		Preloader:        c.Tool.Preloader(),
		ToolState:        c.Tool.State(),
		Send:             send,
	})
	if err != nil {
		return c, err
	}
	return c, connectModules(c)
}
