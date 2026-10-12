package routes

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
)

// Binding registers the business capabilities of a protocol, independently of
// configured providers and their clients.
type Binding struct {
	APIType   llm.APIType // Protocol whose conversation material these capabilities handle.
	Loop      dialogue.Loop
	Compactor contextmgr.Compactor
	Material  session.MaterialPreparer
}
