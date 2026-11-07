// remote_protocol.go 定义远程 CLI 消息与通信协议。

package remote

import (
	"time"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	runtimestatus "elbot/internal/runtime"
)

const (
	defaultRemotePath       = "/cli/v1/ws"
	remoteMsgHello          = "hello"
	remoteMsgHelloOK        = "hello_ok"
	remoteMsgInput          = "input"
	remoteMsgComplete       = "complete"
	remoteMsgCompleteResult = "complete_result"
	remoteMsgChat           = "chat"
	remoteMsgAttachment     = "attachment"
	remoteMsgNotice         = "notice"
	remoteMsgStatus         = "status"
	remoteMsgStream         = "stream"
	remoteMsgError          = "error"
	remoteWriteTimeout      = 15 * time.Second
)

type remoteMessage struct {
	MediaToken string                 `json:"media_token,omitempty"` // 当前 WS 连接的 HTTP 附件凭据，仅 hello_ok 返回。
	FileIDs    []string               `json:"file_ids,omitempty"`    // input 引用的当前连接附件，顺序即输入顺序。
	Attachment *remoteAttachment      `json:"attachment,omitempty"`  // attachment 输出的下载描述。
	StreamID   uint64                 `json:"stream_id,omitempty"`   // Scope for ordered preview updates and reset.
	Event      *delivery.StreamEvent  `json:"event,omitempty"`       // Message stream operation.
	Type       string                 `json:"type"`
	ID         string                 `json:"id,omitempty"`
	ClientID   string                 `json:"client_id,omitempty"`
	Token      string                 `json:"token,omitempty"`
	Text       string                 `json:"text,omitempty"`
	Level      string                 `json:"level,omitempty"`
	Cursor     int                    `json:"cursor,omitempty"`
	Items      []completion.Item      `json:"items,omitempty"`
	Snapshot   runtimestatus.Snapshot `json:"snapshot,omitempty"`
}

// remoteAttachment 只暴露连接级附件身份，不向客户端交付 MediaID 或磁盘路径。
type remoteAttachment struct {
	ID       string `json:"id"`        // 当前连接授权的随机附件 ID。
	Name     string `json:"name"`      // 下载和展示使用的文件名。
	MIMEType string `json:"mime_type"` // Media 归档后的内容类型。
	Size     int64  `json:"size"`      // 归档后的实际字节数。
	Kind     string `json:"kind"`      // image 或 file。
}
