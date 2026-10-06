package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"elbot/internal/llm/httpclient"
)

// Event preserves the native event and its typed views without Chat conversion.
type Event struct {
	Raw            json.RawMessage `json:"-"`
	Type           string          `json:"type"`
	SequenceNumber int             `json:"sequence_number"`
	OutputIndex    int             `json:"output_index"`
	ContentIndex   int             `json:"content_index"`
	SummaryIndex   int             `json:"summary_index"`
	ItemID         string          `json:"item_id"`
	Delta          string          `json:"delta"`
	Arguments      string          `json:"arguments"`
	Text           string          `json:"text"`
	Refusal        string          `json:"refusal"`
	Item           *Item           `json:"item"`
	Response       *Response       `json:"response"`
	Code           string          `json:"code"`
	Message        string          `json:"message"`
	Param          string          `json:"param"`
	Error          error           `json:"-"`
}

func (e *Event) UnmarshalJSON(raw []byte) error {
	var header struct {
		Type           string `json:"type"`
		SequenceNumber int    `json:"sequence_number"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	*e = Event{Type: header.Type, SequenceNumber: header.SequenceNumber}
	if !knownEvent(header.Type) {
		e.Raw = append(json.RawMessage(nil), raw...)
		return nil
	}
	type view Event
	var parsed view
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*e = Event(parsed)
	e.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

func knownEvent(kind string) bool {
	switch kind {
	case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete",
		"response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_text.delta", "response.reasoning_text.done", "error":
		return true
	}
	return false
}

func (e Event) MarshalJSON() ([]byte, error) {
	if !json.Valid(e.Raw) {
		return nil, fmt.Errorf("event requires its native JSON")
	}
	return e.Raw, nil
}

func readStream(ctx context.Context, sse *httpclient.SSE, out chan<- Event) {
	defer close(out)
	defer sse.Close()
	send := func(event Event) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for frame := range sse.Frames {
		if frame.Err != nil {
			err := frame.Err
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			send(Event{Error: fmt.Errorf("response stream: %w", err)})
			return
		}
		var event Event
		if err := json.Unmarshal(frame.Data, &event); err != nil {
			send(Event{Error: fmt.Errorf("parse response event: %w", err)})
			return
		}
		if event.Type == "" || (frame.Event != "" && frame.Event != event.Type) {
			send(Event{Error: fmt.Errorf("invalid response event type %q (SSE event %q)", event.Type, frame.Event)})
			return
		}
		terminal := false
		switch event.Type {
		case "response.completed":
			terminal = true
			if event.Response == nil || event.Response.Status != "completed" || event.Response.ID == "" {
				event.Error = fmt.Errorf("invalid completed response")
			}
		case "response.failed", "response.incomplete":
			terminal = true
			reason := ""
			if event.Response != nil {
				if event.Response.Error != nil {
					reason = httpclient.SafeSummary([]byte(event.Response.Error.Message))
				}
				if event.Response.IncompleteDetails != nil {
					reason = event.Response.IncompleteDetails.Reason
				}
			}
			if event.Response != nil && event.Response.Error != nil {
				event.Error = event.Response.Error
			} else {
				event.Error = fmt.Errorf("%s: %s", event.Type, reason)
			}
		case "error":
			terminal = true
			event.Error = &APIError{Code: event.Code, Message: event.Message, Param: event.Param}
		}
		if !send(event) || terminal {
			return
		}
	}
}
