package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"elbot/internal/app"
	"elbot/internal/launcher"
)

var version = "dev"

func main() {
	startedAt := time.Now()
	opts, err := launcher.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "elbot: %v\n\n", err)
		launcher.WriteUsage(os.Stderr)
		os.Exit(2)
	}
	if opts.Help {
		launcher.WriteUsage(os.Stdout)
		return
	}
	if opts.Version {
		fmt.Fprintf(os.Stdout, "elbot %s\n", version)
		return
	}
	if opts.Command == launcher.CommandCompletion {
		if err := launcher.WriteCompletion(os.Stdout, opts.Completion); err != nil {
			fmt.Fprintf(os.Stderr, "elbot: %v\n", err)
			os.Exit(2)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.Command == launcher.CommandClient {
		if err := app.RunClient(ctx, app.ClientOptions{ConfigPath: opts.ConfigPath, ClientName: opts.ClientName}); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(os.Stderr, "elbot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if opts.Command == launcher.CommandAuto {
		if err := app.TryRunClient(ctx, app.ClientOptions{ConfigPath: opts.ConfigPath, ClientName: opts.ClientName}); err == nil {
			return
		} else if !errors.Is(err, app.ErrClientFallback) {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(os.Stderr, "elbot: %v\n", err)
			os.Exit(1)
		}
	}

	mode := app.RunModeTUI
	if opts.Command == launcher.CommandService {
		mode = app.RunModeService
	}
	if err := app.Run(ctx, app.Options{
		ConfigPath: opts.ConfigPath,
		Version:    version,
		StartedAt:  startedAt,
		Mode:       mode,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "elbot: %v\n", err)
		os.Exit(1)
	}
}
