// 定义 Hook 运行态的共享查询结果，不承载运行时控制状态。

package share

type RuntimeInfo struct {
	ID          string // Plugin identifier used by management commands.
	Description string // User-facing plugin description.
	Mode        string // Display value of the runtime-owned execution mode.
	Status      string // Display value of the runtime-owned lifecycle status.
	Detail      string // Runtime-provided state detail or failure reason.
	Active      int    // Number of active invocations in this snapshot.
	Waiting     int    // Number of waiting continuation leases in this snapshot.
}
