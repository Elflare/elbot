package chatcompletions

import (
	"context"
	"elbot/internal/llm/httpclient"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type accumToolCall struct {
	id   string
	name string
	args strings.Builder
}

func (a *Client) readStream(ctx context.Context, sse *httpclient.SSE, out chan<- Chunk) {
	defer close(out)
	defer sse.Close()
	send := func(c Chunk) bool {
		select {
		case out <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}
	accums := map[int]*accumToolCall{}
	for frame := range sse.Frames {
		if frame.Err != nil {
			err := frame.Err
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			send(Chunk{Error: fmt.Errorf("read stream: %w", err)})
			return
		}
		if string(frame.Data) == "[DONE]" {
			return
		}
		var raw openAIStreamChunk
		if err := json.Unmarshal(frame.Data, &raw); err != nil {
			send(Chunk{Error: fmt.Errorf("parse stream chunk: %w", err)})
			return
		}
		if raw.Usage != nil && len(raw.Choices) == 0 {
			if !send(Chunk{Usage: toUsage(raw.Usage)}) {
				return
			}
			continue
		}
		if len(raw.Choices) == 0 {
			continue
		}
		choice := raw.Choices[0]
		chunk := Chunk{DeltaContent: choice.Delta.Content, DeltaReasoningContent: choice.Delta.ReasoningContent}
		if choice.FinishReason != nil {
			chunk.FinishReason = *choice.FinishReason
		}
		if raw.Usage != nil {
			chunk.Usage = toUsage(raw.Usage)
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc := accums[tc.Index]
			if acc == nil {
				acc = &accumToolCall{}
				accums[tc.Index] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.args.WriteString(tc.Function.Arguments)
		}
		if chunk.FinishReason != "" && len(accums) > 0 {
			indices := make([]int, 0, len(accums))
			for index := range accums {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			for _, index := range indices {
				acc := accums[index]
				chunk.ToolCallDeltas = append(chunk.ToolCallDeltas, ToolCallDelta{Index: index, ID: acc.id, Name: acc.name, Args: acc.args.String()})
			}
		}
		if !send(chunk) {
			return
		}
	}
}
