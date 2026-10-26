package routes

import (
	agentshare "elbot/internal/agent/share"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
)

// Binding registers the business capabilities of a protocol, independently of
// configured providers and their clients.
type Binding struct {
	APIType   llm.APIType // Protocol whose conversation material these capabilities handle.
	Loop      agentshare.Loop
	Compactor contextmgr.Compactor
	Material  session.MaterialPreparer
}
