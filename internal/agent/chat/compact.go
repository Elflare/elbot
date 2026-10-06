package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type Compactor struct {
	Store    storage.Store
	Models   *modelmgr.Service
	Contexts *contextmgr.Service
	Loader   contextmgr.Loader
}

func (s *Compactor) Prepare(ctx context.Context, current *storage.Session, reason string, fallback modelmgr.Selection) (*contextmgr.PreparedCompact, error) {
	selection := s.Models.ResolveCompact(fallback)
	state, err := contextmgr.DecodeState(current.Metadata)
	if err != nil {
		return nil, err
	}
	loaded, err := s.Contexts.Load(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if len(loaded.Messages) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史消息")
	}
	raw, err := s.Loader.LoadRawMessages(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	messages, err := s.CompactMessages(ctx, loaded)
	if err != nil {
		return nil, err
	}
	if selection.Client == nil {
		return nil, fmt.Errorf("压缩模型未配置")
	}
	compressor := Compressor{ClientFor: func(string) llm.Client { return selection.Client }}
	result, err := compressor.Compact(ctx, CompactRequest{Provider: selection.Provider, Model: selection.Model, Messages: messages, UserInputs: compactUserInputs(raw)})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	from := loaded.Messages[0].ID
	if loaded.Summary != nil && loaded.Summary.FromMessageID != "" {
		from = loaded.Summary.FromMessageID
	}
	title, generation, base := contextmgr.NextCompactedTitle(current, state.Compact)
	seed := &contextmgr.CompactState{Pending: true, Summary: result.AssembledSummary, SourceSessionID: current.ID, FromMessageID: from, ToMessageID: loaded.Messages[len(loaded.Messages)-1].ID,
		Provider: selection.Provider, Model: selection.Model, TriggerReason: reason, Generation: generation, BaseTitle: base}
	if result.Usage != nil {
		seed.SourceTokens = result.Usage.PromptTokens
		seed.SummaryTokens = result.Usage.CompletionTokens
		seed.TotalTokens = result.Usage.TotalTokens
		seed.CacheHitTokens = result.Usage.CacheHitTokens
	}
	return &contextmgr.PreparedCompact{Title: title, State: seed}, nil
}

// Only the transcript fields consumed by compression are decoded here.
func compactMessageMetadata(raw string) struct {
	ToolCalls []llm.ToolCallRequest `json:"tool_calls"`
	RawText   string                `json:"raw_text"`
} {
	var result struct {
		ToolCalls []llm.ToolCallRequest `json:"tool_calls"`
		RawText   string                `json:"raw_text"`
	}
	_ = json.Unmarshal([]byte(raw), &result)
	return result
}

func (r *Compactor) CompactMessages(ctx context.Context, loaded *contextmgr.LoadedContext) ([]CompactMessage, error) {
	callIDs := []string{}
	for _, message := range loaded.Messages {
		if message.Role != storage.RoleAssistant {
			continue
		}
		for _, call := range compactMessageMetadata(message.Metadata).ToolCalls {
			if call.ID != "" {
				callIDs = append(callIDs, call.ID)
			}
		}
	}
	successful := map[string]bool{}
	if len(callIDs) > 0 {
		if r.Store == nil || r.Store.ToolCalls() == nil {
			return nil, fmt.Errorf("tool call repository is not configured")
		}
		var err error
		successful, err = r.Store.ToolCalls().SuccessfulIDs(ctx, callIDs)
		if err != nil {
			return nil, err
		}
	}

	out := make([]CompactMessage, 0, len(loaded.Messages))
	summaryInjected := false
	for _, message := range loaded.Messages {
		switch message.Role {
		case storage.RoleUser:
			content := message.Content
			if loaded.Summary != nil && !summaryInjected {
				content = strings.TrimSpace(loaded.Summary.Summary) + "\n\n当前用户输入：\n" + content
				summaryInjected = true
			}
			if strings.TrimSpace(content) != "" {
				out = append(out, CompactMessage{Role: storage.RoleUser, Content: content})
			}
		case storage.RoleAssistant:
			metadata := compactMessageMetadata(message.Metadata)
			content := message.Content
			if metadata.RawText != "" {
				content = metadata.RawText
			}
			calls := make([]CompactToolCall, 0, len(metadata.ToolCalls))
			for _, call := range metadata.ToolCalls {
				if !successful[call.ID] {
					continue
				}
				calls = append(calls, CompactToolCall{Name: call.Name, Arguments: call.Arguments})
			}
			if strings.TrimSpace(content) != "" || len(calls) > 0 {
				out = append(out, CompactMessage{Role: storage.RoleAssistant, Content: content, ToolCalls: calls})
			}
		}
	}
	if loaded.Summary != nil && !summaryInjected && strings.TrimSpace(loaded.Summary.Summary) != "" {
		out = append([]CompactMessage{{Role: storage.RoleUser, Content: loaded.Summary.Summary}}, out...)
	}
	return out, nil
}

func compactUserInputs(messages []storage.Message) []string {
	inputs := []string{}
	for _, message := range messages {
		if message.Role == storage.RoleUser && strings.TrimSpace(message.Content) != "" {
			inputs = append(inputs, message.Content)
		}
	}
	return inputs
}
