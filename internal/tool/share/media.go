// 定义工具共用的媒体参数与精简结果，不暴露媒体存储记录。
package share

import (
	"bytes"
	"encoding/json"
	"fmt"

	"elbot/internal/media"
)

// MediaInput 显式声明工具输入的媒体，不从命令文本推断。
type MediaInput struct {
	MediaID string `json:"media"` // 媒体中心分配的媒体 ID。
}

func (i *MediaInput) UnmarshalJSON(data []byte) error {
	type input MediaInput
	var value input
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if !media.ValidID(value.MediaID) {
		return fmt.Errorf("invalid media")
	}
	*i = MediaInput(value)
	return nil
}

// MediaInfo 是工具可用的通用媒体信息，不包含存储位置或图片内部属性。
type MediaInfo struct {
	ID       string // 媒体引用 ID。
	Name     string // 展示及导出时使用的文件名。
	MIMEType string // 媒体内容类型。
	Size     int64  // 媒体内容的字节数。
}

// HistoryMediaResult 保留历史媒体请求的顺序及逐项错误。
type HistoryMediaResult struct {
	Media MediaInfo // 成功取得的媒体；失败时为零值。
	Err   error     // 此项查询或获取失败的原因。
}
