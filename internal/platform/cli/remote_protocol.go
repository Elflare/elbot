// remote_protocol.go 定义远程 CLI 消息与通信协议。

package cli

import (
	"time"

	"elbot/internal/completion"
	"elbot/internal/delivery"
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
	remoteMsgStatus         = "status"
	remoteMsgStream         = "stream"
	remoteMsgError          = "error"
	remoteWriteTimeout      = 15 * time.Second
)

type remoteMessage struct {
	StreamID uint64                 `json:"stream_id,omitempty"` // Scope for ordered preview updates and reset.
	Event    *delivery.StreamEvent  `json:"event,omitempty"`     // Message stream operation.
	Type     string                 `json:"type"`
	ID       string                 `json:"id,omitempty"`
	ClientID string                 `json:"client_id,omitempty"`
	Token    string                 `json:"token,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Level    string                 `json:"level,omitempty"`
	Cursor   int                    `json:"cursor,omitempty"`
	Items    []completion.Item      `json:"items,omitempty"`
	Snapshot runtimestatus.Snapshot `json:"snapshot,omitempty"`
}
