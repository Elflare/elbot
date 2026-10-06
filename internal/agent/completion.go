package agent

import (
	"context"

	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// CompletionService exposes the structured completion service for platform adapters.
func (a *Agent) CompletionService() *completion.Service {
	return a.completion
}

func newCompletion(commands *command.Router, sessions *session.Service, turns *turn.Manager, store storage.Store, identity *identityResolver, registry *tool.Registry, preloader *toolrun.PreloadService) *completion.Service {
	return completion.NewService(
		completion.RiskConfirmationSource{Router: commands, Sessions: sessions, Turns: turns, Scope: identity.Scope, CommandNames: riskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: commands, Sessions: sessions, Store: store, Scope: identity.Scope},
		completion.ToolDirectiveSource{
			Registry: func() *tool.Registry { return registry },
			Actor:    identity.Actor,
			Policy:   func() *security.Policy { return identity.policy },
			Tags: func(ctx context.Context, _ *tool.Registry, actor contextinfo.Actor, policy *security.Policy) []string {
				return preloader.Tags(contextinfo.WithActor(security.WithPolicy(ctx, policy), actor))
			},
			ToolNamesByTag: func(ctx context.Context, _ *tool.Registry, tag string, allowed func(tool.Tool) bool) []string {
				return preloader.ToolNamesByTag(ctx, tag, allowed)
			},
		},
		completion.RouterSource{Router: commands, Actor: identity.Actor},
	)
}
