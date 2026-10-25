package background

import (
	"context"
	"sync"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/elnis"
	"elbot/internal/background/execution"
	background "elbot/internal/background/share"
	"elbot/internal/config"
)

// Supervisor owns background producers; borrowed Dialogue and Tool services outlive it.
type Supervisor struct {
	ctx             context.Context
	cancel          context.CancelFunc
	cfg             *config.Config
	cronManager     *elcron.Manager
	cron            *elcron.Service
	runner          *execution.BackgroundRunner
	elnis           *elnis.Runtime
	enableWorkers   bool
	mu              sync.Mutex
	started, closed bool
	startupDone     chan struct{}
	elnisDone       chan struct{}
	done            chan struct{}
}

func (m *Supervisor) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	return m.runner.RunBackground(ctx, req)
}
