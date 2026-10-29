// 待删除：仅余待迁到调用方的输入识别和展示函数。
package turn

import (
	"fmt"
	"sort"
	"strings"
)

func IsConfirm(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "$", "是", "y", "yes":
		return true
	default:
		return false
	}
}
func IsCancel(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "取消", "否", "n", "no":
		return true
	default:
		return false
	}
}
func ToolsString(tools map[string]int) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s x%d", name, tools[name]))
	}
	return strings.Join(parts, ", ")
}
