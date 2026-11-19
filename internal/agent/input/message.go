package input

import (
	"context"
	"elbot/internal/operationresult"
	"reflect"
	"strings"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
)

type MessageHandler struct {
	deps MessageHandlerDeps
}

// HandleMessage dispatches commands and chat messages.
func (h *MessageHandler) HandleMessage(ctx context.Context, text string) (context.Context, bool, error) {
	segments := agentshare.InboundSegments(ctx, text)
	originalText := llm.SegmentsTextOnly(segments)
	woken := h.messageWakeup(ctx, originalText)
	ctx = agentshare.WithWakeup(ctx, woken)
	message := agentshare.Message{Role: string(llm.RoleUser), Segments: segments}
	if strings.TrimSpace(originalText) == "/cancel" {
		if h.deps.Hooks.CancelRoute(ctx, message) {
			_, err := h.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "Hook canceled"})
			return ctx, false, err
		}
	}
	waiting := h.deps.Hooks.HasRoute(ctx, message)
	if woken || waiting {
		prepared, err := h.materializePlatformMedia(ctx)
		if err != nil {
			return ctx, false, err
		}
		ctx = prepared
		segments = agentshare.InboundSegments(ctx, text)
		message.Segments = segments
	}
	result, err := h.deps.Hooks.PlatformMessageReceived(ctx, message)
	if err != nil {
		return ctx, false, err
	}
	if len(result.Outputs) > 0 {
		if _, err := h.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: result.Outputs}); err != nil {
			return ctx, false, err
		}
	}
	if result.Consume {
		return ctx, false, nil
	}
	hookChangedSegments := !reflect.DeepEqual(result.Segments, segments)
	if hookChangedSegments {
		segments = result.Segments
	} else {
		segments = inboundContextSegments(ctx, text)
	}
	ctx = agentshare.WithInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)
	message.Segments = result.Segments
	if strings.TrimSpace(text) == "/cancel" && h.deps.Hooks.CancelRoute(ctx, message) {
		_, err := h.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "Hook canceled"})
		return ctx, false, err
	}
	if !woken {
		return ctx, false, nil
	}
	text = agentshare.StripWakeupPrefix(ctx, text)
	segments = replaceInboundTextSegments(ctx, text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	ctx, handled, err := h.deps.Commands.Handle(ctx, text)
	if handled {
		return ctx, true, err
	}
	next, err := h.deps.Input.HandleInput(ctx, text)
	return next, false, err
}

type MessageHooks interface {
	CancelRoute(context.Context, agentshare.Message) bool
	HasRoute(context.Context, agentshare.Message) bool
	PlatformMessageReceived(context.Context, agentshare.Message) (agentshare.PlatformMessageReceivedResult, error)
}

type MessageCommands interface {
	Handle(context.Context, string) (context.Context, bool, error)
	IsCommand(string) bool
}

type MessageInput interface {
	HandleInput(context.Context, string) (context.Context, error)
}

// MessageHandlerDeps declares only the capabilities consumed by MessageHandler.
type MessageHandlerDeps struct {
	History interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	}
	Hooks    MessageHooks
	Output   InputOutput
	Commands MessageCommands
	Input    MessageInput
	Media    interface {
		ImportPlatform(context.Context, string, func(context.Context, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error), media.PlatformMedia) (operationresult.Result[media.MediaInfo], error)
		AssociateHistory(context.Context, media.HistoryMessage, int, string, string) error
	}
	Messages interface {
		FindByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error)
	}
	MediaRows interface {
		FindOutputs(context.Context, string, contextinfo.ChatType, string, string, time.Time) ([]storage.MediaOutput, error)
	}
}

// NewMessageHandler initializes MessageHandler without installing cross-service callbacks.
func replaceInboundTextSegments(ctx context.Context, text string) []llm.MessageSegment {
	segments := agentshare.InboundSegments(ctx, text)
	if llm.SegmentsTextOnly(segments) == text {
		return segments
	}
	var original strings.Builder
	for _, segment := range segments {
		if segment.Type == llm.SegmentText {
			original.WriteString(segment.Text)
		}
	}
	before, after := []rune(original.String()), []rune(text)
	if len(before) == 0 {
		if text != "" {
			return append([]llm.MessageSegment{{Type: llm.SegmentText, Text: text}}, segments...)
		}
		return segments
	}
	// Locate the changed text span, retaining the surrounding text in its
	// original segments. Prefix/directive removal must not move quoted images.
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	end, newEnd := len(before), len(after)
	for end > start && newEnd > start && before[end-1] == after[newEnd-1] {
		end--
		newEnd--
	}
	replacement := string(after[start:newEnd])
	out := make([]llm.MessageSegment, 0, len(segments))
	offset := 0
	inserted := false
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			out = append(out, segment)
			continue
		}
		runes := []rune(segment.Text)
		left := min(max(start-offset, 0), len(runes))
		right := min(max(end-offset, 0), len(runes))
		updated := string(runes[:left])
		if !inserted && offset+len(runes) >= start {
			updated += replacement
			inserted = true
		}
		updated += string(runes[right:])
		offset += len(runes)
		if updated != "" {
			segment.Text = updated
			out = append(out, segment)
		}
	}
	return out
}

func inboundContextSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := agentshare.MessageContextFrom(ctx); ok {
		if len(msg.ContextSegments) > 0 {
			return agentshare.PlatformSegmentsToLLM(msg.ContextSegments, msg.ContextText)
		}
		if strings.TrimSpace(msg.ContextText) != "" {
			return llm.TextSegments(msg.ContextText)
		}
	}
	return agentshare.InboundSegments(ctx, text)
}

func NewMessageHandler(deps MessageHandlerDeps) *MessageHandler {
	return &MessageHandler{deps: deps}
}
