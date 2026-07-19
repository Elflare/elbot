package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/hook"
	hookoutput "elbot/internal/hook/output"
)

const Version = "hook.v2"

type Frame struct {
	Type   string          `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type EventResultBase struct {
	Status string `json:"status"`
	hookoutput.Group
	PassThrough *bool          `json:"pass_through,omitempty"`
	Message     *MessageResult `json:"message,omitempty"`
}

type MessageResult struct {
	Text     *string                      `json:"text,omitempty"`
	Segments *[]hookoutput.MessageSegment `json:"segments,omitempty"`
}

type EventHandleParams struct {
	Event hook.Event        `json:"event"`
	Match hook.MatchContext `json:"match,omitempty"`
}

func NewRequest(id, method string, params any) (Frame, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Type: "request", ID: id, Method: method, Params: raw}, nil
}

func DecodeFrame(data []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func EncodeFrame(frame any) ([]byte, error) {
	return json.Marshal(frame)
}

func ValidateID(id, prefix string) error {
	if !strings.HasPrefix(id, prefix) {
		return fmt.Errorf("frame id %q must use %s prefix", id, prefix)
	}
	return nil
}
