package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

type Compactor struct {
	ToolCalls interface {
		SuccessfulIDs(context.Context, []string) (map[string]bool, error)
	}
	Models interface {
		ResolveCompact(modelmgr.Selection) modelmgr.Selection
	}
	Contexts interface {
		Load(context.Context, string) (*contextmgr.LoadedContext, error)
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
	loaded, err := s.Contexts.Load(ctx, current.ID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if len(loaded.Messages) == 0 {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Rejected, Message: "无可压缩历史"}, nil
	}
	raw, err := s.Loader.LoadRawMessages(ctx, current.ID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	userInputs, err := compactUserInputs(raw, state.Compact)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	messages, err := s.CompactMessages(ctx, loaded)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if selection.Client == nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Unavailable, Message: "压缩模型未配置"}, nil
	}
	compressor := Compressor{ClientFor: func(string) llm.Client { return selection.Client }}
	result, err := compressor.Compact(ctx, CompactRequest{Provider: selection.Provider, Model: selection.Model, Messages: messages, UserInputs: userInputs})
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	from := loaded.Messages[0].ID
	if loaded.Summary != nil && loaded.Summary.FromMessageID != "" {
		from = loaded.Summary.FromMessageID
	}
	title, generation, base := contextmgr.NextCompactedTitle(current, state.Compact)
	seed := &contextmgr.CompactState{Pending: true, Summary: result.Summary, UserInputs: userInputs, SourceSessionID: current.ID, FromMessageID: from, ToMessageID: loaded.Messages[len(loaded.Messages)-1].ID,
		Provider: selection.Provider, Model: selection.Model, TriggerReason: reason, Generation: generation, BaseTitle: base}
	if result.Usage != nil {
		seed.SourceTokens = result.Usage.PromptTokens
		seed.SummaryTokens = result.Usage.CompletionTokens
		seed.TotalTokens = result.Usage.TotalTokens
		seed.CacheHitTokens = result.Usage.CacheHitTokens
	}
	return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Success, Data: &contextmgr.PreparedCompact{Title: title, State: seed}}, nil
}

func (r *Compactor) CompactMessages(ctx context.Context, loaded *contextmgr.LoadedContext) ([]CompactMessage, error) {
	callIDs := []string{}
	for _, message := range loaded.Messages {
		if message.Role != storage.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				callIDs = append(callIDs, call.ID)
			}
		}
	}
	successful := map[string]bool{}
	if len(callIDs) > 0 {
		var err error
		successful, err = r.ToolCalls.SuccessfulIDs(ctx, callIDs)
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
			content := message.Content
			if message.SourceContent != "" {
				content = message.SourceContent
			}
			calls := make([]CompactToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
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

const compactUserInputMetadataKey = "context_compact_user_input"

func setCompactUserInput(message *storage.Message, input string) error {
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return err
	}
	if err := fields.Set(compactUserInputMetadataKey, input); err != nil {
		return err
	}
	message.Metadata, err = fields.Encode()
	return err
}

func compactUserInput(message storage.Message) (string, error) {
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return "", err
	}
	raw, ok := fields[compactUserInputMetadataKey]
	if !ok {
		return message.Content, nil
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("decode compacted user input metadata: %w", err)
	}
	return input, nil
}

func compactUserInputs(messages []storage.Message, previous *contextmgr.CompactState) ([]string, error) {
	inputs := []string{}
	if previous != nil {
		inputs = append(inputs, previous.UserInputs...)
	}
	for _, message := range messages {
		if message.Role != storage.RoleUser {
			continue
		}
		input, err := compactUserInput(message)
		if err != nil {
			return nil, fmt.Errorf("read user input from message %s: %w", message.ID, err)
		}
		if strings.TrimSpace(input) != "" {
			inputs = append(inputs, input)
		}
	}
	return inputs, nil
}
