package dialogue

import (
	"context"
)

type AssistantPreparer interface {
	PrepareAssistant(context.Context, string) (string, error)
}
