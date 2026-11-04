package agent

import (
	"context"
	"elbot/internal/media"
)

func publishMediaUsageFinished(center *media.MediaCenter) func(context.Context, media.UsageID) {
	return func(ctx context.Context, id media.UsageID) {
		center.PublishUsageFinished(ctx, []media.UsageID{id})
	}
}
