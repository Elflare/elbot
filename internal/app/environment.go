package app

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	platformbuiltin "elbot/internal/platform/builtin"
)

type startupProfiler struct {
	enabled   bool
	startedAt time.Time
	last      time.Time
	entries   []startupProfileEntry
}

type startupProfileEntry struct {
	name     string
	duration time.Duration
	total    time.Duration
}

func newStartupProfiler(startedAt time.Time) *startupProfiler {
	now := time.Now()
	if startedAt.IsZero() {
		startedAt = now
	}
	return &startupProfiler{startedAt: startedAt, last: startedAt}
}

func (p *startupProfiler) SetEnabled(enabled bool) {
	p.enabled = enabled
}

func (p *startupProfiler) Mark(name string) {
	now := time.Now()
	duration := now.Sub(p.last)
	total := now.Sub(p.startedAt)
	p.last = now
	if p.enabled {
		p.entries = append(p.entries, startupProfileEntry{name: name, duration: duration, total: total})
	}
}

func (p *startupProfiler) Flush() time.Duration {
	total := time.Since(p.startedAt)
	if p.enabled {
		for _, entry := range p.entries {
			fmt.Fprintf(os.Stderr, "[startup] %-24s took=%s total=%s\n", entry.name, entry.duration, entry.total)
		}
	}
	fmt.Fprintf(os.Stderr, "elbot startup completed in %s\n", total)
	return total
}

type defaultEnvironment struct{}

func (defaultEnvironment) ResolveMode(mode RunMode) (RunMode, error) {
	return resolveRunMode(mode)
}

func (defaultEnvironment) ClaimServiceMarker(mode RunMode) (io.Closer, error) {
	if mode != RunModeService {
		return nil, nil
	}
	return claimServiceMarker()
}

func (defaultEnvironment) NewStartupProfiler(startedAt time.Time) StartupProfiler {
	return newStartupProfiler(startedAt)
}

func startupProfileEnabled(level string) bool {
	return strings.EqualFold(strings.TrimSpace(level), "debug")
}

func resolveRunMode(mode RunMode) (RunMode, error) {
	switch mode {
	case "", RunModeAuto:
		if runtime.GOOS != "windows" && serviceMarkerRunning() {
			fmt.Fprintln(os.Stderr, "ElBot service detected, starting local CLI-only mode. Use `elbot run` to force full foreground mode.")
			return RunModeCLIOnly, nil
		}
		fmt.Fprintln(os.Stderr, "No ElBot service detected, starting full foreground mode. Use `elbot cli` to start local CLI only.")
		return RunModeFull, nil
	case RunModeFull, RunModeCLIOnly, RunModeService:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown run mode %q", mode)
	}
}

func platformMode(mode RunMode) platformbuiltin.Mode {
	switch mode {
	case RunModeCLIOnly:
		return platformbuiltin.ModeCLIOnly
	case RunModeService:
		return platformbuiltin.ModeService
	default:
		return platformbuiltin.ModeFull
	}
}

func shouldStartCron(mode RunMode) bool {
	return mode != RunModeCLIOnly
}
