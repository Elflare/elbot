package chat_history

import (
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/registry"
)

func TestViewImageAvailabilityByAPIType(t *testing.T) {
	for _, apiType := range []string{"", "chat", "response"} {
		ctx := contextinfo.WithModel(t.Context(), contextinfo.Model{APIType: apiType})
		available := registry.InfoAvailableInContext(ctx, (ViewImageTool{}).Definition().Definition)
		if available != (apiType == "response") {
			t.Fatalf("%s available=%t", apiType, available)
		}
	}
}
