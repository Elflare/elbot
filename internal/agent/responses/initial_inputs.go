package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
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
	users := make([]storage.NativeInput, 0, len(inputs))
	notices := 0
	for _, input := range inputs {
		item, _, err := decodeQueuedInput(input)
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
		users = append(users, input)
	}
	if notices > 1 {
		return missing()
	}
	inputs = users
	if len(inputs) != len(messages) {
		return missing()
	}
	for i, message := range messages {
		input := inputs[i]
		if message.Role != storage.RoleUser || message.SessionID != sessionID || input.SessionID != sessionID || input.MessageID != message.ID || input.ExchangeID != "" || input.CallID != "" || input.ConsumedBy != "" {
			return missing()
		}
		segments, err := inputSegments(input)
		if err != nil {
			return err
		}
		if llm.SegmentsContentText(segments) != message.Content || dialogue.StoredMessageSegments(segments) != message.Segments {
			return missing()
		}
		item, err := api.ParseItem([]byte(input.ItemJSON))
		if err != nil {
			return err
		}
		want, err := nativeItem("", segments)
		if err != nil {
			return err
		}
		var gotJSON, wantJSON any
		if json.Unmarshal(item.Raw, &gotJSON) != nil || json.Unmarshal(want.Raw, &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
			return missing()
		}
	}
	return nil
}
