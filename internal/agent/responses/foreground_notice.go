package responses

import (
	"context"
	"encoding/json"

	"elbot/internal/agent/dialogue"
	"elbot/internal/storage"
)

func foregroundNoticeInput(row *storage.Session) (*storage.NativeInput, error) {
	message, ok := dialogue.ForegroundNotice(row)
	if !ok {
		return nil, nil
	}
	input, err := queuedInput(row.ID, "", "", "", message.Segments)
	return &input, err
}

func isForegroundNotice(input storage.NativeInput, expected *storage.NativeInput) bool {
	return expected != nil && input.SessionID == expected.SessionID && input.MessageID == "" && input.ExchangeID == "" && input.CallID == "" && input.ConsumedBy == "" && input.ItemJSON == expected.ItemJSON && input.MediaJSON == expected.MediaJSON
}

func (s *turnState) queueForegroundNotice(ctx context.Context, inputs []storage.NativeInput) ([]storage.NativeInput, error) {
	notice, err := foregroundNoticeInput(s.session)
	if err != nil || notice == nil {
		return inputs, err
	}
	for _, input := range inputs {
		if isForegroundNotice(input, notice) {
			return inputs, nil
		}
	}
	native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{*notice}}
	if err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Native: &native}, "append_foreground_notice"); err != nil {
		return nil, err
	}
	return append(inputs, *notice), nil
}

func inputOrder(input storage.NativeInput) int {
	if input.CallID != "" {
		return 0
	}
	var header struct{ Type string }
	if json.Unmarshal([]byte(input.ItemJSON), &header) == nil && header.Type == "additional_tools" {
		return 2
	}
	if input.MessageID == "" {
		return 3
	}
	return 1
}
