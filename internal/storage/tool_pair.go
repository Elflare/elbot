package storage

import "encoding/json"

const ToolResultMessageKey = "tool_result_message_id"

func ToolResultMessageID(message Message) (string, error) {
	fields, err := DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return "", err
	}
	var id string
	if raw, ok := fields[ToolResultMessageKey]; ok {
		err = json.Unmarshal(raw, &id)
	}
	return id, err
}
