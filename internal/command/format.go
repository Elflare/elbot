package command

import "elbot/internal/command/share"

// Format 组装已确定前缀、命令名及参数，不读取配置或解析展示文本。
func Format(prefix, name string, args ...string) string {
	return share.Format(prefix, name, args...)
}
