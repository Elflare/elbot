package share

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

func (e *UnavailableError) Error() string { return "media unavailable: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// IsUnavailable 仅接受纯不可用错误；组合错误中的任一独立故障都会阻止降级。
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*UnavailableError); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		items := joined.Unwrap()
		if len(items) == 0 {
			return false
		}
		for _, item := range items {
			if !IsUnavailable(item) {
				return false
			}
		}
		return true
	}
	return IsUnavailable(errors.Unwrap(err))
}

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
