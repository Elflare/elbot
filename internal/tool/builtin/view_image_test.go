package builtin

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/registry"
)

func TestViewImageHistoryHintUsesAPIType(t *testing.T) {
	ctx := context.Background()
	available := func(ctx context.Context) bool { return registry.InfoAvailableInContext(ctx, (ViewImageTool{}).Info()) }
	for _, apiType := range []string{"", "chat", "response"} {
		ctx := contextinfo.WithModel(ctx, contextinfo.Model{APIType: apiType})
		hint := viewImageHistoryHint(ctx, available)
		if (hint != "") != (apiType == "response") {
			t.Fatalf("%s hint=%q", apiType, hint)
		}
	}
}
