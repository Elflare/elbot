// Package crondef defines the Cron metadata persisted by Tool and read by Background.
package crondef

const (
	UserHandlerName = "builtin.cron"
	MetadataKind    = "llm_cron"
)

type Metadata struct {
	Kind      string          `json:"kind"`          // 持久化定义的种类。
	Version   int             `json:"version"`       // 定义格式版本。
	Title     string          `json:"title"`         // 展示标题。
	CreatedBy CronActor       `json:"created_by"`    // 创建者身份快照。
	Schedule  CronSchedule    `json:"schedule"`      // 一次性或周期计划。
	Trigger   CronTrigger     `json:"trigger"`       // 直接通知或 LLM 任务。
	Target    CronTarget      `json:"target"`        // 报告投递范围。
	LLM       CronLLMMetadata `json:"llm,omitempty"` // LLM 模型和工具选择。
}

type ScheduleMode string

const (
	ScheduleOnce = "once"
	ScheduleCron = "cron"
)

type TriggerMode string

const (
	TriggerDirect = "direct"
	TriggerLLM    = "llm"
)

type CronActor struct {
	Platform string `json:"platform"`       // 创建者所属平台。
	ID       string `json:"id"`             // 平台用户标识。
	Name     string `json:"name,omitempty"` // 创建时的展示名称。
}

type CronSchedule struct {
	Mode     ScheduleMode `json:"mode"`                // 计划模式。
	RunAt    string       `json:"run_at,omitempty"`    // 本地时间，格式为 YYYY-MM-DD HH:MM:SS。
	CronExpr string       `json:"cron_expr,omitempty"` // 五字段 Cron 表达式。
}

type CronTrigger struct {
	Mode    TriggerMode `json:"mode"`    // 执行方式。
	Message string      `json:"message"` // 通知正文或 ELyph 任务文本。
}

type CronTarget struct {
	ID                  string `json:"id,omitempty"`          // 来源平台的私聊接收人 ID；空值沿用默认接收人。
	AllEnabledPlatforms bool   `json:"all_enabled_platforms"` // 是否向全部启用平台投递。
	SourcePlatform      string `json:"source_platform"`       // 非广播时的来源平台。
}

type CronLLMMetadata struct {
	ToolListNames []string `json:"tool_list_names,omitempty"` // 工具、Skill 或标签选择。
	ModelProvider string   `json:"model_provider,omitempty"`  // 可选的模型提供方。
	Model         string   `json:"model,omitempty"`           // 与提供方配对的模型名称。
}

// CronTaskStatus 是 DeliveryState 中供管理工具读取的状态投影。
// 后台保存投递进度时同时写入；查询方不重新执行投递完成判定。
type CronTaskStatus struct {
	DeliveryComplete bool `json:"delivery_complete,omitempty"` // 后台已判定本轮报告投递完成或被前台接管。
}
