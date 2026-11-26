package storage

import (
	"encoding/json"

	"elbot/internal/storage/maindb"
)

type SessionMetadata = maindb.SessionMetadata

// UserInputMetadata links a stored user row to its live input echoes.
type UserInputMetadata struct {
	InputIDs []string `json:"input_ids,omitempty"`
}

func EncodeInputIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	data, _ := json.Marshal(UserInputMetadata{InputIDs: ids})
	return string(data)
}

func DecodeSessionMetadata(raw string) (SessionMetadata, error) {
	return maindb.DecodeSessionMetadata(raw)
}
