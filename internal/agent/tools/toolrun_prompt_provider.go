package tools

import (
	"context"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type toolRunPromptProvider struct{}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, _ session.Scope) (agentshare.PromptToolNames, error) {
	if err := ctx.Err(); err != nil {
		return agentshare.PromptToolNames{}, err
	}
	if mode != storage.SessionModeWork || agentshare.IsBackgroundSession(session) {
		return agentshare.PromptToolNames{}, nil
	}
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	return agentshare.PromptToolNames{Tools: availability.Tools, Skills: availability.Skills}, nil
}
