package cron

import (
	"elbot/internal/crondef"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/delivery"
)

type DeliveryStatus string

const (
	DeliveryPending           DeliveryStatus = "pending"
	DeliveryDelivered         DeliveryStatus = "delivered"
	DeliveryFallbackPending   DeliveryStatus = "fallback_pending"
	DeliveryFallbackDelivered DeliveryStatus = "fallback_delivered"
)

type CronDeliveryState struct {
	crondef.CronTaskStatus
	ExecutionRunID  string                    `json:"execution_run_id,omitempty"`
	TakenOver       bool                      `json:"taken_over,omitempty"`
	Outcome         string                    `json:"outcome,omitempty"`
	ExecutionResult string                    `json:"execution_result,omitempty"`
	RunID           string                    `json:"run_id"`
	ReportReady     bool                      `json:"report_ready,omitempty"`
	TaskCompleted   bool                      `json:"task_completed,omitempty"`
	NeedReport      bool                      `json:"need_report"` // 是否发送汇报正文；附件保存与投递不受此开关影响。
	Report          string                    `json:"report,omitempty"`
	ReportSegments  []delivery.Output         `json:"report_segments,omitempty"` // 明确来源的附件，恢复发送不重新解析协议。
	ReportSessionID string                    `json:"report_session_id,omitempty"`
	ReportMessageID string                    `json:"report_message_id,omitempty"`
	Targets         []CronDeliveryTargetState `json:"targets,omitempty"`
}

type CronDeliveryTargetState struct {
	Target  delivery.Target           `json:"target"` // 平台和会话共同标识投递目标。
	Outputs []CronDeliveryOutputState `json:"outputs,omitempty"`
}

type CronDeliveryOutputState struct {
	ID           string         `json:"id"`
	Status       DeliveryStatus `json:"status"`
	FallbackText string         `json:"fallback_text,omitempty"`
}

type CronLLMResult = background.JSONResult

func decodeMetadata(raw string) (crondef.Metadata, error) {
	var meta crondef.Metadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return crondef.Metadata{}, err
	}
	if meta.Kind != crondef.MetadataKind {
		return crondef.Metadata{}, fmt.Errorf("unsupported cron metadata kind %q", meta.Kind)
	}
	return meta, nil
}

func parseRunAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("run_at is required")
	}
	runAt, err := time.ParseInLocation(time.DateTime, value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
	}
	return runAt, nil
}

var invalidSandboxNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func cronSandboxSubdir(jobName string) string {
	name := strings.TrimPrefix(crondef.NormalizeJobName(jobName), "user.cron.")
	name = invalidSandboxNameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_-")
	if name == "" {
		name = "default"
	}
	return filepath.ToSlash(filepath.Join("cron", name))
}
