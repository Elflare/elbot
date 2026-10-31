// 提供 Hook 多个子包共用的日志文本辅助。
package share

import "elbot/internal/logging"

// TruncateLogContent bounds a log-only string to a positive Unicode character
// limit, including the ellipsis. Callers choose the content and limit; business
// results and execution arguments must remain unchanged.
func TruncateLogContent(text string, limit int) string {
	return logging.TruncateContent(text, limit)
}
