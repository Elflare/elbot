package storage

import "encoding/json"

const ToolResultMessageKey = "tool_result_message_ids"

// ToolResultMessageIDs returns the ordered result boundaries of one call batch.
func ToolResultMessageIDs(message Message) ([]string, error) {
	fields, err := DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return nil, err
	}
	var ids []string
	if raw, ok := fields[ToolResultMessageKey]; ok {
		err = json.Unmarshal(raw, &ids)
	}
	return ids, err
}
