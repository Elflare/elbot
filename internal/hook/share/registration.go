// 定义内部共用的 Hook 注册、查询、重载报告和唤醒策略契约。

package share

import "fmt"

// Registration describes one explicitly matched hook handler.
type Registration struct {
	Point       Point
	Priority    int
	PluginID    string
	Name        string
	Description string
	Match       Match
	Block       BlockPolicy
	Handler     Handler
	Detail      string
	Wakeup      WakeupPolicy
}

// Info describes a registered hook for inspection commands.
type Info struct {
	PluginID    string
	Name        string
	Description string
	Point       Point
	Priority    int
	Detail      string
	Active      int
}

type ReloadReport struct {
	Notices []string
}

// Registrar registers hook handlers for modules.
type Registrar interface {
	Register(reg Registration)
}

// WakeupPolicy controls whether a hook accepts woken or unwoken messages.
type WakeupPolicy string

const (
	// WakeupRequired accepts only woken messages and is the default.
	WakeupRequired WakeupPolicy = "required"
	// WakeupAny accepts messages regardless of wakeup state.
	WakeupAny WakeupPolicy = "any"
	// WakeupForbidden accepts only unwoken messages.
	WakeupForbidden WakeupPolicy = "forbidden"
)

// Validate reports whether policy is a supported wakeup policy.
func (policy WakeupPolicy) Validate() error {
	switch policy {
	case "", WakeupRequired, WakeupAny, WakeupForbidden:
		return nil
	default:
		return fmt.Errorf("unsupported wakeup policy %q", policy)
	}
}
