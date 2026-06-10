package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"elbot/internal/app"
)

const version = "dev"

func main() {
	startedAt := time.Now()
	configPath := flag.String("config", "", "path to TOML config file; empty uses ELBOT_CONFIG_FILE, platform config dir, then config/app.toml")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, app.Options{
		ConfigPath: *configPath,
		Version:    version,
		StartedAt:  startedAt,
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "elbot: %v\n", err)
		os.Exit(1)
	}
}
