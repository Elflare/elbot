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
	Kind OriginKind
	Name string
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

func (o Origin) Label() string {
	if strings.TrimSpace(o.Name) == "" {
		return string(o.Kind)
	}
	return string(o.Kind) + ":" + strings.TrimSpace(o.Name)
}
