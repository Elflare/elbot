package execution

import (
	"context"
	"os"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/workspace"
)

// BackgroundRunner prepares the task workspace and calls Agent's public execution capability.
type BackgroundRunner struct {
	execution   BackgroundExecution // Borrowed Agent capability; Agent owns turn preparation and execution.
	sandboxRoot string              // Configured root for restricted task directories.
}

func (r *BackgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	dir, err := workspace.SandboxDir(r.sandboxRoot, req.SandboxSubdir)
	if err != nil {
		return background.RunResult{}, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return background.RunResult{}, err
	}
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: dir, Restricted: true})

	result, err := r.execution.RunBackground(ctx, agent.BackgroundRequest{
		TargetPlatforms: req.TargetPlatforms,
		TaskType:        string(req.TaskType), TaskID: req.TaskID, Name: req.Name,
		Actor: req.Actor, SessionID: req.SessionID,
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

// NewBackgroundRunner initializes BackgroundRunner without installing cross-service callbacks.
func NewBackgroundRunner(deps BackgroundRunnerDeps) *BackgroundRunner {
	return &BackgroundRunner{execution: deps.Execution, sandboxRoot: deps.SandboxRoot}
}
