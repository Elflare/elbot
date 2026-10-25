package builtin

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	toolmedia "elbot/internal/tool/media"
)

func TestViewImageHistoryHintUsesAPIType(t *testing.T) {
	ctx := context.Background()
	center := toolmedia.NewMediaRuntime(nil, "")
	for _, apiType := range []string{"", "chat", "response"} {
		ctx := contextinfo.WithModel(ctx, contextinfo.Model{APIType: apiType})
		hint := viewImageHistoryHint(ctx, center)
		if (hint != "") != (apiType == "response") {
			t.Fatalf("%s hint=%q", apiType, hint)
		}
	}
}
