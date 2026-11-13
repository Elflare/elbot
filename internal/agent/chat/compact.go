package chat

import (
	"context"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

type Compactor struct {
	ToolCalls interface {
		SuccessfulIDs(context.Context, string, []string) (map[string]bool, error)
	}
	Models interface {
		ResolveCompact(modelmgr.Selection) modelmgr.Selection
	}
	Loader interface {
		LoadRawMessages(context.Context, string) ([]storage.Message, error)
	}
}

func (s *Compactor) Prepare(ctx context.Context, current *storage.Session, reason string, fallback modelmgr.Selection) (operationresult.Result[*contextmgr.PreparedCompact], error) {
	selection := s.Models.ResolveCompact(fallback)
	state, err := contextmgr.DecodeState(current.Metadata)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	raw, err := s.Loader.LoadRawMessages(ctx, current.ID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if len(raw) == 0 {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Rejected, Message: "无可压缩历史"}, nil
	}
	userInputs := compactUserInputs(raw, state.Compact, state.Compact != nil && state.Compact.Generation > 0)
	messages, err := s.CompactMessages(ctx, raw)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if selection.Client == nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Unavailable, Message: "压缩模型未配置"}, nil
	}
	compressor := Compressor{ClientFor: func(string) llm.Client { return selection.Client }}
	result, err := compressor.Compact(ctx, CompactRequest{Provider: selection.Provider, Model: selection.Model, Messages: messages})
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	from := raw[0].ID
	title, generation, base := contextmgr.NextCompactedTitle(current, state.Compact)
	seed := &contextmgr.CompactState{Inputs: userInputs, SourceSessionID: current.ID, FromMessageID: from, ToMessageID: raw[len(raw)-1].ID,
		Provider: selection.Provider, Model: selection.Model, TriggerReason: reason, Generation: generation, BaseTitle: base}
	if result.Usage != nil {
		seed.SourceTokens = result.Usage.PromptTokens
		seed.SummaryTokens = result.Usage.CompletionTokens
		seed.TotalTokens = result.Usage.TotalTokens
		seed.CacheHitTokens = result.Usage.CacheHitTokens
	}
	return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Success, Data: &contextmgr.PreparedCompact{Title: title, State: seed, InitialMessage: assembleSummary(result.Summary, userInputs)}}, nil
}

func (r *Compactor) CompactMessages(ctx context.Context, history []storage.Message) ([]CompactMessage, error) {
	callIDs := map[string][]string{}
	for _, message := range history {
		if message.Role != storage.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				callIDs[message.SessionID] = append(callIDs[message.SessionID], call.ID)
			}
		}
	}
	successful := map[string]map[string]bool{}
	for sessionID, ids := range callIDs {
		idsForSession, err := r.ToolCalls.SuccessfulIDs(ctx, sessionID, ids)
		if err != nil {
			return nil, err
		}
		successful[sessionID] = idsForSession
	}

	out := make([]CompactMessage, 0, len(history))
	for _, message := range history {
		switch message.Role {
		case storage.RoleUser:
			content := message.Content
			if strings.TrimSpace(content) != "" {
				out = append(out, CompactMessage{Role: storage.RoleUser, Content: content})
			}
		case storage.RoleAssistant:
			content := message.Content
			if message.SourceContent != "" {
				content = message.SourceContent
			}
			calls := make([]CompactToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if !successful[message.SessionID][call.ID] {
					continue
				}
				calls = append(calls, CompactToolCall{Name: call.Name, Arguments: call.Arguments})
			}
			if strings.TrimSpace(content) != "" || len(calls) > 0 {
				out = append(out, CompactMessage{Role: storage.RoleAssistant, Content: content, ToolCalls: calls})
			}
		}
	}
	return out, nil
}

func compactUserInputs(messages []storage.Message, previous *contextmgr.CompactState, skipFirstUser bool) []string {
	inputs := []string{}
	if previous != nil {
		inputs = append(inputs, previous.Inputs...)
	}
	for _, message := range messages {
		if message.Role != storage.RoleUser {
			continue
		}
		if skipFirstUser {
			skipFirstUser = false
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			inputs = append(inputs, message.Content)
		}
	}
	return inputs
}
