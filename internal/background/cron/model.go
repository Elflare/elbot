package cron

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/llm"
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
	ScheduleOnce ScheduleMode = "once"
	ScheduleCron ScheduleMode = "cron"
)

type TriggerMode string

const (
	TriggerDirect TriggerMode = "direct"
	TriggerLLM    TriggerMode = "llm"
)

type CronActor struct {
	ActorID        string `json:"actor_id"`               // 项目内身份标识。
	Platform       string `json:"platform"`               // 创建者所属平台。
	PlatformUserID string `json:"platform_user_id"`       // 平台用户标识。
	DisplayName    string `json:"display_name,omitempty"` // 创建时的展示名称。
}

type CronSchedule struct {
	Mode     ScheduleMode `json:"mode"`                // 计划模式。
	RunAt    string       `json:"run_at,omitempty"`    // 本地时间，格式为 timeLayout。
	CronExpr string       `json:"cron_expr,omitempty"` // 五字段 Cron 表达式。
}

type CronTrigger struct {
	Mode    TriggerMode `json:"mode"`    // 执行方式。
	Message string      `json:"message"` // 通知正文或 ELyph 任务文本。
}

type CronTarget struct {
	AllEnabledPlatforms bool   `json:"all_enabled_platforms"` // 是否向全部启用平台投递。
	SourcePlatform      string `json:"source_platform"`       // 非广播时的来源平台。
}

type CronLLMMetadata struct {
	ToolListNames []string `json:"tool_list_names,omitempty"` // 工具、Skill 或标签选择。
	ModelProvider string   `json:"model_provider,omitempty"`  // 可选的模型提供方。
	Model         string   `json:"model,omitempty"`           // 与提供方配对的模型名称。
}

type DeliveryStatus string

const (
	DeliveryPending           DeliveryStatus = "pending"
	DeliveryDelivered         DeliveryStatus = "delivered"
	DeliveryFallbackPending   DeliveryStatus = "fallback_pending"
	DeliveryFallbackDelivered DeliveryStatus = "fallback_delivered"
)

type CronDeliveryState struct {
	DeliveryComplete bool                      `json:"delivery_complete,omitempty"` // 本轮报告已投递完成或被前台接管，供管理工具读取。
	ExecutionRunID   string                    `json:"execution_run_id,omitempty"`
	TakenOver        bool                      `json:"taken_over,omitempty"`
	Outcome          string                    `json:"outcome,omitempty"`
	ExecutionResult  string                    `json:"execution_result,omitempty"`
	RunID            string                    `json:"run_id"`
	ReportReady      bool                      `json:"report_ready,omitempty"`
	TaskCompleted    bool                      `json:"task_completed,omitempty"`
	Report           string                    `json:"report,omitempty"`
	ReportSegments   []llm.MessageSegment      `json:"report_segments,omitempty"`
	ReportSessionID  string                    `json:"report_session_id,omitempty"`
	ReportMessageID  string                    `json:"report_message_id,omitempty"`
	Targets          []CronDeliveryTargetState `json:"targets,omitempty"`
}

type CronDeliveryTargetState struct {
	Key     string                    `json:"key"`
	Outputs []CronDeliveryOutputState `json:"outputs,omitempty"`
}

type CronDeliveryOutputState struct {
	ID           string         `json:"id"`
	Status       DeliveryStatus `json:"status"`
	FallbackText string         `json:"fallback_text,omitempty"`
}

type CronLLMResult = background.JSONResult

func decodeMetadata(raw string) (Metadata, error) {
	var meta Metadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return Metadata{}, err
	}
	if meta.Kind != metadataKind {
		return Metadata{}, fmt.Errorf("unsupported cron metadata kind %q", meta.Kind)
	}
	return meta, nil
}

func parseRunAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("run_at is required")
	}
	runAt, err := time.ParseInLocation(timeLayout, value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
	}
	return runAt, nil
}

func normalizeJobName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return ""
	}
	return "user.cron." + name
}

func cronSandboxSubdir(jobName string) string {
	name := strings.TrimPrefix(normalizeJobName(jobName), "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_-")
	if name == "" {
		name = "default"
	}
	return filepath.ToSlash(filepath.Join("cron", name))
}

func cronScopeID(jobName string) string { return "cron:" + normalizeJobName(jobName) }
