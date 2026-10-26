package execution

import (
	"context"

	"elbot/internal/agent"
)

// BackgroundExecution is the Agent capability consumed by this task runner.
type BackgroundExecution interface {
	RunBackground(context.Context, agent.BackgroundRequest) (agent.BackgroundResult, error)
}

// BackgroundRunnerDeps declares only the capabilities consumed by BackgroundRunner.
type BackgroundRunnerDeps struct {
	Execution   BackgroundExecution // Prepares and executes the task through Agent's public boundary.
	SandboxRoot string              // Configured root used to prepare restricted task directories.
}
