// origin.go 定义调用来源及其校验、标识，不承担事件鉴权或执行。

package elvena

import (
	"fmt"
	"strings"
)

type OriginKind string

const (
	OriginHTTPToken OriginKind = "http_token"
	OriginHook      OriginKind = "hook"
	OriginInternal  OriginKind = "internal"
)

type Origin struct {
	Kind OriginKind `json:"kind"` // 已认证入口类型。
	Name string     `json:"name"` // Token、Hook 或内部入口名称，不作为行为者身份。
}

func (o Origin) Validate() error {
	switch o.Kind {
	case OriginHTTPToken, OriginHook, OriginInternal:
	default:
		return fmt.Errorf("unsupported elvena origin kind %q", o.Kind)
	}
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("elvena origin name is required")
	}
	return nil
}
