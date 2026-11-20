// 声明 Hook 对媒体中心的消费契约。

package hook

import (
	"context"
	"elbot/internal/operationresult"
	"io"

	"elbot/internal/delivery"
	"elbot/internal/media"
)

// MediaAPI is the host-only media capability. It is never serialized to hook.v2.
// Go hooks share the center rather than accessing its storage implementation.
type MediaAPI interface {
	ImportSource(context.Context, delivery.Source, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
	Hold(context.Context, string) (operationresult.Result[*media.HeldMedia], error)
	PublishUsageFinished(context.Context, []media.UsageID)
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
	Info(context.Context, string) (operationresult.Result[media.MediaInfo], error)
	AddReference(context.Context, media.Reference) error
	RemoveReference(context.Context, media.Reference) error
}

var _ MediaAPI = (*media.MediaCenter)(nil)
