// 定义流水线逐次调用的唤醒、只读策略和实际 handler 登记能力。

package pipeline

import "context"

type CallOptions struct {
	Woken         bool                                                    // Fixed wakeup result supplied by the entry boundary.
	ReadOnlyCalls bool                                                    // Passed to rules to forbid changes to call identities and arguments.
	Observe       func(context.Context, string) (context.Context, func()) // Optional per-handler request lifetime; execution owns its timing.
}
