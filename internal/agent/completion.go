package agent

import (
	"context"

	"elbot/internal/agent/dialogue"
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

// CompletionDeps declares the query sources consumed by structured completion.
type CompletionDeps struct {
	Commands  *command.Router
	Sessions  *session.Service
	Turns     *turn.Manager
	Store     storage.Store
	Identity  CompletionIdentity
	Registry  *tool.Registry
	Preloader *toolrun.PreloadService
	Execution CompletionExecution
}
type CompletionIdentity interface {
	dialogue.Identity
	Policy() *security.Policy
}
type CompletionExecution interface {
	ModelContext(context.Context, *storage.Session) (context.Context, error)
}

func NewCompletion(deps CompletionDeps) *completion.Service {
	commands, sessions, turns, store, identity, registry, preloader, execution := deps.Commands, deps.Sessions, deps.Turns, deps.Store, deps.Identity, deps.Registry, deps.Preloader, deps.Execution

	return completion.NewService(
		completion.RiskConfirmationSource{Router: commands, Sessions: sessions, Turns: turns, Scope: identity.Scope, CommandNames: riskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: commands, Sessions: sessions, Store: store, Scope: identity.Scope},
		completion.ToolDirectiveSource{
			Context: func(ctx context.Context) (context.Context, error) {
				row, err := sessions.Current(ctx, identity.Scope(ctx))
				if err != nil && err != storage.ErrNotFound {
					return ctx, err
				}
				prepared, err := execution.ModelContext(ctx, row)
				if err != nil {
					return ctx, err
				}
				return prepared, nil
			},
			Registry: func() *tool.Registry { return registry },
			Actor:    identity.Actor,
			Policy:   func() *security.Policy { return identity.Policy() },
			Tags: func(ctx context.Context, _ *tool.Registry, actor contextinfo.Actor, policy *security.Policy) ([]string, error) {
				return preloader.Tags(contextinfo.WithActor(security.WithPolicy(ctx, policy), actor))
			},
			ToolNamesByTag: func(ctx context.Context, _ *tool.Registry, tag string, allowed func(tool.Tool) bool) ([]string, error) {
				return preloader.ToolNamesByTag(ctx, tag, allowed)
			},
		},
		completion.RouterSource{Router: commands, Actor: identity.Actor},
	)
}
