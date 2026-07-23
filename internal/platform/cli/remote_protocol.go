package cli

import (
	"time"

	"elbot/internal/completion"
	runtimestatus "elbot/internal/runtime"
)

const (
	remoteMsgHello          = "hello"
	remoteMsgHelloOK        = "hello_ok"
	remoteMsgInput          = "input"
	remoteMsgComplete       = "complete"
	remoteMsgCompleteResult = "complete_result"
	remoteMsgChat           = "chat"
	remoteMsgNotice         = "notice"
	remoteMsgReasoning      = "reasoning"
	remoteMsgStatus         = "status"
	remoteMsgStreamAppend   = "stream_append"
	remoteMsgStreamReplace  = "stream_replace"
	remoteMsgStreamFinish   = "stream_finish"
	remoteMsgError          = "error"
	remoteWriteTimeout      = 15 * time.Second
)

type remoteMessage struct {
	Type     string                 `json:"type"`
	ID       string                 `json:"id,omitempty"`
	ClientID string                 `json:"client_id,omitempty"`
	Token    string                 `json:"token,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Cursor   int                    `json:"cursor,omitempty"`
	Items    []completion.Item      `json:"items,omitempty"`
	Snapshot runtimestatus.Snapshot `json:"snapshot,omitempty"`
}
