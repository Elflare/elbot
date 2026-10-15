package app

import (
	"context"
	"time"
)

type RunMode string

const (
	RunModeAuto    RunMode = "auto"
	RunModeFull    RunMode = "full"
	RunModeCLIOnly RunMode = "cli"
	RunModeService RunMode = "service"
)

type Options struct {
	ConfigPath string
	Version    string
	StartedAt  time.Time
	Mode       RunMode
}

// Run starts ElBot with the production dependency set.
func Run(ctx context.Context, opts Options) error {
	runner := NewRunner(DefaultDependencies())
	return runner.Run(ctx, opts)
}
