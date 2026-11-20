package cli

import (
	"context"

	clishare "elbot/internal/platform/cli/share"
)

type testDisplayTarget struct{ id string }

func (t testDisplayTarget) TargetID() string                                   { return t.id }
func (testDisplayTarget) PresentStatus(context.Context, clishare.Status) error { return nil }
