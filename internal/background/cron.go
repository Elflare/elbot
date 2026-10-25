package background

import (
	"time"

	"elbot/internal/contextinfo"
)

// 对外 Cron 数据契约与 Tool 已有连接约定一致。
// TODO: 后续实现 Supervisor 的 CreateCron、UpdateCron、GetCron、ListCrons、DisableCron、DeleteCron 入口。
type CronCreateRequest struct {
	Name                string            // 任务短名。
	Title               string            // 任务展示标题。
	ScheduleMode        string            // once 或 cron，合法性由后台业务校验。
	RunAt               string            // 已换算的本地绝对时间。
	CronExpr            string            // 周期任务的 Cron 表达式。
	TriggerMode         string            // direct 或 llm，合法性由后台业务校验。
	Message             string            // 通知正文或 LLM 任务文本。
	ToolListNames       []string          // LLM 任务请求的工具、Skill 或标签名。
	ModelProvider       string            // 可选的任务专用模型提供方。
	Model               string            // 与提供方配对的模型名称。
	AllEnabledPlatforms bool              // 是否向全部启用平台发送。
	Enabled             bool              // 创建后的启用状态。
	Actor               contextinfo.Actor // 发起管理操作的公共身份。
	SourcePlatform      string            // 未广播时使用的来源平台。
}

// CronUpdateRequest 的 nil 字段表示不修改；非 nil 的零值或空切片表示显式清空。
type CronUpdateRequest struct {
	Name                string            // 要修改的任务短名。
	Title               *string           // 可选的新标题。
	ScheduleMode        *string           // 可选的新计划模式。
	RunAt               *string           // 可选的已换算绝对时间。
	CronExpr            *string           // 可选的新 Cron 表达式。
	TriggerMode         *string           // 可选的新触发模式。
	Message             *string           // 可选的新任务文本。
	ToolListNames       *[]string         // 可选的工具名单；空切片清空名单。
	ModelProvider       *string           // 可选模型提供方；与 Model 配对清空以恢复默认。
	Model               *string           // 可选模型名称。
	AllEnabledPlatforms *bool             // 可选的广播设置。
	Enabled             *bool             // 可选的启用状态。
	Actor               contextinfo.Actor // 发起管理操作的公共身份。
}

// CronInfo 只承载任务管理信息，不包含内部执行、投递或存储状态。
type CronInfo struct {
	Name                string            `json:"name"`                      // 任务短名。
	Title               string            `json:"title"`                     // 展示标题。
	ScheduleMode        string            `json:"schedule_mode"`             // 当前计划模式。
	RunAt               string            `json:"run_at,omitempty"`          // 一次性任务时间。
	CronExpr            string            `json:"cron_expr,omitempty"`       // 周期表达式。
	TriggerMode         string            `json:"trigger_mode"`              // 当前触发模式。
	Message             string            `json:"message"`                   // 任务文本。
	ToolListNames       []string          `json:"tool_list_names,omitempty"` // 任务的工具名单。
	ModelProvider       string            `json:"model_provider,omitempty"`  // 任务专用模型提供方。
	Model               string            `json:"model,omitempty"`           // 任务专用模型名称。
	AllEnabledPlatforms bool              `json:"all_enabled_platforms"`     // 是否广播。
	SourcePlatform      string            `json:"source_platform"`           // 来源平台。
	CreatedBy           contextinfo.Actor `json:"created_by"`                // 任务创建者。
	Enabled             bool              `json:"enabled"`                   // 是否启用。
	Completed           bool              `json:"completed"`                 // 任务是否已完成。
	RunCount            int               `json:"run_count"`                 // 已运行次数。
	LastRunAt           *time.Time        `json:"last_run_at,omitempty"`     // 最近运行时间。
	NextRunAt           *time.Time        `json:"next_run_at,omitempty"`     // 下次计划时间。
	CreatedAt           time.Time         `json:"created_at"`                // 创建时间。
	UpdatedAt           time.Time         `json:"updated_at"`                // 最近更新时间。
}
