package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/elnis"
)

type defaultIntegrationFactory struct{}

func (defaultIntegrationFactory) Attach(ctx context.Context, req IntegrationRequest) (PlatformComponents, error) {
	foundation := req.Foundation
	runtime := req.Runtime
	platforms := req.Platforms
	cfg := foundation.Config
	if runtime.Agent == nil {
		return PlatformComponents{}, fmt.Errorf("app: integration runtime has no agent")
	}

	if cfg.Elnis.Enabled && req.Mode != RunModeCLIOnly {
		if runtime.ElvenaBus == nil {
			return PlatformComponents{}, fmt.Errorf("app: integration runtime has no elvena bus")
		}
		elnisTokens, err := resolveElnisTokens(cfg)
		if err != nil {
			return PlatformComponents{}, err
		}
		elnisService, err := elnis.NewService(elnis.Options{
			Config:           cfg.Elnis,
			SandboxRoot:      cfg.Sandbox.Root,
			Tokens:           elnisTokens,
			Store:            foundation.Store,
			Logger:           foundation.Logs.Elnis(),
			EnabledPlatforms: runtimeNames(platforms.Runtimes),
			PlatformCallers:  platformCallerResolver{runtimes: platforms.Runtimes},
			Audit:            auditFunc(foundation.Logs),
			Send: func(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
				return runtime.Agent.SendNotice(ctx, target, []delivery.Output{out})
			},
			Runner: runtime.Agent,
			ResolveModel: func(slot string) config.ModelSelection {
				selected := runtime.Agent.CurrentModelForMode(slot)
				return config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
			},
		})
		if err != nil {
			return PlatformComponents{}, err
		}
		runtime.ElvenaBus.SetDispatcher(elnisService)
		platforms.Runtimes = append(platforms.Runtimes, elnisRuntimeAdapter{runtime: elnis.NewRuntime(cfg.Elnis.HTTP, elnisService)})
		req.Profiler.Mark("elnis init")
	}

	registerCompletionPlatforms(runtime.Agent, platforms.Runtimes)
	registerCommandCatalogs(runtime.Agent, platforms.Runtimes)
	registerPlatformHooks(runtime.Agent, platforms.Runtimes)
	req.Profiler.Mark("platform hooks")
	return platforms, nil
}

func resolveElnisTokens(cfg *config.Config) (map[string]string, error) {
	out := map[string]string{}
	configDir := filepath.Dir(cfg.ConfigPath)
	for name, tokenCfg := range cfg.Elnis.Tokens {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, envName := range tokenCfg.TokenEnv {
			envName = strings.TrimSpace(envName)
			if envName == "" {
				continue
			}
			value, ok, err := config.ConfigEnv(envName, configDir)
			if err != nil {
				return nil, fmt.Errorf("resolve elnis token %q from %s: %w", name, envName, err)
			}
			if ok && strings.TrimSpace(value) != "" {
				out[name] = strings.TrimSpace(value)
				break
			}
		}
	}
	return out, nil
}

func runtimeNames(runtimes []platformRuntime) []string {
	out := []string{}
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		name := strings.TrimSpace(runtime.Name())
		if name != "" && name != "elnis" {
			out = append(out, name)
		}
	}
	return out
}
