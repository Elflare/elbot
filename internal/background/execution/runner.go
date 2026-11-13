package execution

import (
	"context"
	"elbot/internal/operationresult"
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

func (r *BackgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (context.Context, operationresult.Result[background.RunResult], error) {
	dir, err := workspace.SandboxDir(r.sandboxRoot, req.SandboxSubdir)
	if err != nil {
		return ctx, operationresult.Result[background.RunResult]{}, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ctx, operationresult.Result[background.RunResult]{}, err
	}
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: dir, Restricted: true})

	ctx, result, err := r.execution.RunBackground(ctx, agent.BackgroundRequest{
		TargetPlatforms: req.TargetPlatforms,
		TaskType:        string(req.TaskType), TaskID: req.TaskID, Name: req.Name,
		Actor: req.Actor, SessionID: req.SessionID,
		ModelProvider: req.ModelProvider, Model: req.Model,
		Prompt: req.Prompt, PromptSegments: req.PromptSegments, NeedsElyphRules: req.NeedsElyphRules,
		ToolListNames: req.ToolListNames, AllowedToolNames: req.AllowedToolNames,
		ELwispTools: req.ELwispTools, Metadata: req.Metadata,
	})
	if err != nil {
		return ctx, operationresult.Result[background.RunResult]{}, err
	}
	if result.Status != operationresult.Success {
		return ctx, operationresult.Result[background.RunResult]{Status: result.Status, Message: result.Message}, nil
	}
	data := result.Data
	return ctx, operationresult.Result[background.RunResult]{Status: operationresult.Success, Data: background.RunResult{
		RunID: data.RunID, SessionID: data.SessionID, MessageID: data.MessageID,
		Text: data.Text, TakenOver: data.TakenOver, Outcome: data.Outcome,
	}}, nil
}

// NewBackgroundRunner initializes BackgroundRunner without installing cross-service callbacks.
func NewBackgroundRunner(deps BackgroundRunnerDeps) *BackgroundRunner {
	return &BackgroundRunner{execution: deps.Execution, sandboxRoot: deps.SandboxRoot}
}
