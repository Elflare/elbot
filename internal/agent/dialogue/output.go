package dialogue

import (
	"context"

	"elbot/internal/events"
)

type AssistantPreparer interface {
	PrepareAssistant(context.Context, string) (string, events.LogResult, error)
}
