package share

import (
	"net/url"
	"path"
	"strings"
)

// ValidID 只检查标识格式，不查询媒体。
func ValidID(id string) bool {
	if !strings.HasPrefix(id, IDPrefix) || len(id) != len(IDPrefix)+64 {
		return false
	}
	for _, char := range id[len(IDPrefix):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

// SanitizeName 清理对象与交付共同使用的展示名称。
func SanitizeName(name string) string {
	value := strings.TrimSpace(name)
	lower := strings.ToLower(value)
	if value == "" || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "base64:") {
		return "file"
	}
	if strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "file:") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "file"
		}
		value = parsed.EscapedPath()
	}
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	if decoded, err := url.PathUnescape(value); err == nil {
		value = path.Base(strings.ReplaceAll(decoded, "\\", "/"))
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || value == "/" {
		return "file"
	}
	return value
}
