package share

// MaintenanceJob 是维护组件生成、Cron 消费的维护任务配置。
type MaintenanceJob struct {
	Name     string // 维护任务的唯一名称。
	Handler  string // 已注册的维护处理器名称。
	Schedule string // Cron 调度表达式。
	Enabled  bool   // 启用并更新任务，或禁用已有任务。
}
