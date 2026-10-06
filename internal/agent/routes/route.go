package routes

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
)

// Binding is a provider registration, not a runtime dependency container.
// A provider without dialogue capabilities still supports Client.GenerateText.
type Binding struct {
	Origin    llm.Origin
	Client    llm.Client
	Loop      dialogue.Loop
	Compactor contextmgr.Compactor
}
