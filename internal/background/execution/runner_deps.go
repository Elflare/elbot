package execution

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/agent"
)

// BackgroundExecution is the Agent capability consumed by this task runner.
type BackgroundExecution interface {
	RunBackground(context.Context, agent.BackgroundRequest) (context.Context, operationresult.Result[agent.BackgroundResult], error)
}
