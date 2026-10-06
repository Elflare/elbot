package dialogue

import (
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type PendingUserMessage struct {
	Message      storage.Message
	MessageIndex int
	PlatformText string
	Segments     []llm.MessageSegment
}

func DrainPending(turns *turn.Manager, sessionID string, expected ...string) *PendingUserMessage {
	pending := turns.DrainMergedInput(sessionID, expected...)
	if pending.Text == "" && len(pending.Segments) == 0 {
		return nil
	}
	segments := append([]llm.MessageSegment(nil), pending.Segments...)
	if len(segments) == 0 {
		segments = llm.TextSegments(pending.Text)
	}
	message := storage.Message{
		ID:        storage.NewID(),
		SessionID: sessionID,
		Role:      storage.RoleUser,
		Content:   llm.SegmentsContentText(segments),
		Segments:  StoredMessageSegments(segments),
	}
	return &PendingUserMessage{Message: message, PlatformText: pending.PlatformText, Segments: segments}
}
