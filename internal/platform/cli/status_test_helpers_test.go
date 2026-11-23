package cli

import (
	"context"
	"testing"

	runtimestatus "elbot/internal/runtime"
)

func startTestDisplay(t *testing.T) (*statusDisplay, func()) {
	d := newStatusDisplay(testPlatformName)
	go d.run()
	stop := func() {
		d.BeginClose()
		_ = d.wait(context.Background())
	}
	t.Cleanup(stop)
	return d, stop
}

type testDisplayTarget struct{ id string }

func (t testDisplayTarget) TargetID() string                                          { return t.id }
func (testDisplayTarget) PresentStatus(context.Context, runtimestatus.Snapshot) error { return nil }
