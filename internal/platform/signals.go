package platform

import (
	"context"
	"elbot/internal/media"
)

func publishMediaUsageFinished(center *media.MediaCenter) func(context.Context, []media.UsageID) {
	return func(ctx context.Context, ids []media.UsageID) {
		center.PublishUsageFinished(ctx, media.UsageFinishedEvent{UsageIDs: ids})
	}
}
