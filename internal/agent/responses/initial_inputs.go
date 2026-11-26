package responses

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

// A failed first model request leaves durable user inputs but no response
// checkpoint. Only those complete, still-queued inputs may start a fresh chain.
func (r *Loop) validateInitialInputs(ctx context.Context, row *storage.Session, messages []storage.Message) error {
	sessionID := row.ID
	inputs, err := r.Repository.PendingInputs(ctx, sessionID)
	if err != nil {
		return err
	}
	missing := func() error {
		return fmt.Errorf("Responses 会话有历史但缺少完整原生 checkpoint、seed 或待提交输入")
	}
	notice, err := foregroundNoticeInput(row)
	if err != nil {
		return err
	}
	type userInput struct {
		storage.NativeInput
		raw      json.RawMessage
		segments []llm.MessageSegment
	}
	users := make([]userInput, 0, len(inputs))
	notices := 0
	for _, input := range inputs {
		item, segments, err := decodeQueuedInput(input)
		if err != nil {
			return err
		}
		if item.Type == "additional_tools" {
			if input.SessionID != sessionID || input.ConsumedBy != "" {
				return missing()
			}
			continue
		}
		if isForegroundNotice(input, notice) {
			notices++
			continue
		}
		users = append(users, userInput{NativeInput: input, raw: item.Raw, segments: segments})
	}
	if notices > 1 {
		return missing()
	}
	if len(users) != len(messages) {
		return missing()
	}
	for i, message := range messages {
		input := users[i]
		if message.Role != llm.RoleUser || message.SessionID != sessionID || input.SessionID != sessionID || input.MessageID != message.ID || input.ExchangeID != "" || input.CallID != "" || input.ConsumedBy != "" {
			return missing()
		}
		if agentshare.MessageContent(input.segments) != message.Content || agentshare.StoredMessageSegments(input.segments) != message.Segments {
			return missing()
		}
		want, err := nativeItem("", input.segments)
		if err != nil {
			return err
		}
		var gotJSON, wantJSON any
		if json.Unmarshal(input.raw, &gotJSON) != nil || json.Unmarshal(want.Raw, &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
			return missing()
		}
	}
	return nil
}
