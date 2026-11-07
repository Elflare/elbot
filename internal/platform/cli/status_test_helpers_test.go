package cli

import (
	"context"

	"elbot/internal/delivery"
	clishare "elbot/internal/platform/cli/share"
)

type testDisplayTarget struct{ id string }

func (t testDisplayTarget) TargetID() string                                 { return t.id }
func (testDisplayTarget) SendReply(context.Context, []delivery.Output) error { return nil }
func (testDisplayTarget) StartStream(context.Context) (delivery.MessageStream, error) {
	return nil, nil
}
func (testDisplayTarget) PresentStatus(context.Context, clishare.Status) error { return nil }
