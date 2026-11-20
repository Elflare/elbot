// remote_protocol.go 定义远程 CLI 消息与通信协议。

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"time"

	"elbot/internal/command"
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
	remoteMsgCommandResult  = "command_result"
	remoteMsgAttachment     = "attachment"
	remoteMsgNotice         = "notice"
	remoteMsgStatus         = "status"
	remoteMsgStream         = "stream"
	remoteMsgError          = "error"
	remoteWriteTimeout      = 15 * time.Second
)

type remoteMessage struct {
	FileNames  []string                `json:"file_names,omitempty"` // user_input 展示的附件名称，不传播连接私有的文件凭据。
	RequestID  string                  `json:"request_id,omitempty"`
	Result     *command.Result         `json:"result,omitempty"`
	Error      *string                 `json:"error,omitempty"`
	MediaToken string                  `json:"media_token,omitempty"` // 当前 WS 连接的 HTTP 附件凭据，仅 hello_ok 返回。
	APIToken   string                  `json:"api_token,omitempty"`   // 当前 WS 连接的管理 API 路由凭据，仅 hello_ok 返回。
	FileIDs    []string                `json:"file_ids,omitempty"`    // input 引用的当前连接附件，顺序即输入顺序。
	Attachment *remoteAttachment       `json:"attachment,omitempty"`  // attachment 输出的下载描述。
	StreamID   uint64                  `json:"stream_id,omitempty"`   // Scope for ordered preview updates and reset.
	Event      *delivery.StreamEvent   `json:"event,omitempty"`       // Message stream operation.
	Type       string                  `json:"type"`
	ID         string                  `json:"id,omitempty"`
	ClientID   string                  `json:"client_id,omitempty"`
	SessionID  string                  `json:"session_id,omitempty"` // input 的显式目标或下行事件所属会话。
	Token      string                  `json:"token,omitempty"`
	Text       string                  `json:"text,omitempty"`
	Blocks     []delivery.ContentBlock `json:"blocks,omitempty"`
	Actions    []delivery.Action       `json:"actions,omitempty"` // 与 chat 正文绑定的操作，由业务统一生成。
	Level      string                  `json:"level,omitempty"`
	Cursor     int                     `json:"cursor,omitempty"`
	Items      []completion.Item       `json:"items,omitempty"`
	Snapshot   runtimestatus.Snapshot  `json:"snapshot,omitempty"`
}

// remoteAttachment 只暴露连接级附件身份，不向客户端交付 MediaID 或磁盘路径。
type remoteAttachment struct {
	ID       string `json:"id"`        // 当前连接授权的随机附件 ID。
	Name     string `json:"name"`      // 下载和展示使用的文件名。
	MIMEType string `json:"mime_type"` // Media 归档后的内容类型。
	Size     int64  `json:"size"`      // 归档后的实际字节数。
	Kind     string `json:"kind"`      // image 或 file。
}

type commandResultMessage struct {
	Type      string         `json:"type"`            // 结构化命令响应帧。
	RequestID string         `json:"request_id"`      // 发起输入的客户端关联 ID。
	Result    command.Result `json:"result"`          // 命令业务结果。
	Error     *string        `json:"error,omitempty"` // 执行故障的原始说明。
}

const cliChunksProtocol = "elbot.cli-chunks"
const cliChunkSize = 32 * 1024

func readServerMessage(ctx context.Context, conn *websocket.Conn, msg *remoteMessage) error {
	kind, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	if kind == websocket.MessageBinary {
		for n := len(data); n == cliChunkSize; {
			k, chunk, err := conn.Read(ctx)
			if err != nil {
				return err
			}
			if k != websocket.MessageBinary {
				return fmt.Errorf("interrupted CLI message")
			}
			data = append(data, chunk...)
			n = len(chunk)
		}
	}
	return json.Unmarshal(data, msg)
}
