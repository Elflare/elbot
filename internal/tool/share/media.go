// 定义工具共用的媒体参数与精简结果，不暴露媒体存储记录。
package share

import (
	"bytes"
	"context"
	"encoding/json"
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

// MediaImportOptions 只包含工具指定的导入展示信息，不暴露媒体中心的来源记录。
type MediaImportOptions struct {
	Name     string // 导入后使用的文件名；为空时由媒体入口推断。
	MIMEType string // 调用方提供的内容类型；为空时由媒体入口识别。
}

// HistoryMediaResult 保留历史媒体请求的顺序及逐项错误。
type HistoryMediaResult struct {
	Media MediaInfo // 成功取得的媒体；失败时为零值。
	Err   error     // 此项查询或获取失败的原因。
}

// MediaCall 是 Builtin 与 Skill 共用的单次媒体资源契约；Close 释放本次持有。
type MediaCall interface {
	Retain(context.Context, string) (MediaInfo, error)
	Workspace(string) (string, error)
	Export(context.Context, string, string) (string, MediaInfo, error)
	Import(context.Context, string, string, MediaImportOptions) (MediaInfo, error)
	CachedExport(context.Context, string) (string, error)
	Close() error
}
