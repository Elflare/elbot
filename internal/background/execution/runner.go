package execution

import (
	"context"
	"os"
	"strings"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"elbot/internal/workspace"
)

// BackgroundRunner prepares the task workspace and calls Agent's public execution capability.
type BackgroundRunner struct {
	execution   BackgroundExecution // Borrowed Agent capability; Agent owns turn preparation and execution.
	sandboxRoot string              // Configured root for restricted task directories.
}

func (r *BackgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	sandboxSubdir := strings.TrimSpace(req.SandboxSubdir)
	if sandboxSubdir == "" {
		sandboxSubdir = strings.TrimSpace(string(req.Kind))
	}
	if sandboxSubdir == "" {
		sandboxSubdir = "background"
	}
	dir, err := workspace.SandboxDir(r.sandboxRoot, sandboxSubdir)
	if err != nil {
		return background.RunResult{}, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return background.RunResult{}, err
	}
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: dir, Restricted: true})
	ctx = contextinfo.WithBackgroundKind(ctx, toolBackgroundKind(req.Kind))

	result, err := r.execution.RunBackground(ctx, agent.BackgroundRequest{
		Kind: string(req.Kind), Name: req.Name, Title: req.Title,
		Platform: req.Platform, Actor: req.Actor, ScopeID: req.ScopeID, SessionID: req.SessionID,
		ModelProvider: req.ModelProvider, Model: req.Model,
		Prompt: req.Prompt, PromptSegments: req.PromptSegments, NeedsElyphRules: req.NeedsElyphRules,
		ToolListNames: req.ToolListNames, AllowedToolNames: req.AllowedToolNames,
		ELwispTools: req.ELwispTools, Metadata: req.Metadata,
	})
	return background.RunResult{
		RunID: result.RunID, SessionID: result.SessionID, MessageID: result.MessageID,
		Text: result.Text, TakenOver: result.TakenOver, Outcome: result.Outcome,
	}, err
}

func toolBackgroundKind(kind background.Kind) contextinfo.BackgroundKind {
	switch kind {
	case background.KindCron:
		return contextinfo.BackgroundKindCron
	case background.KindElnis:
		return contextinfo.BackgroundKindElnis
	default:
		return contextinfo.BackgroundKind(strings.TrimSpace(string(kind)))
	}
}

// NewBackgroundRunner initializes BackgroundRunner without installing cross-service callbacks.
func NewBackgroundRunner(deps BackgroundRunnerDeps) *BackgroundRunner {
	return &BackgroundRunner{execution: deps.Execution, sandboxRoot: deps.SandboxRoot}
}
