package routes

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
)

// Route is a registration record, not a runtime dependency container.
type Route struct {
	Protocol  llm.ProtocolID
	Loop      dialogue.Loop
	Compactor contextmgr.Compactor
}
